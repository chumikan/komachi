package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/perber/wiki/internal/storage/postgres"
)

const PendingFile = ".leafwiki-transfer-pending.json"
const receiptFile = ".leafwiki-import-receipt.json"

var canonicalTables = []string{"pages", "revision_contents", "revision_asset_manifests", "revisions", "users", "sessions", "api_keys", "email_tokens", "favorites", "user_settings", "instance_settings"}
var derivedTables = []string{"links", "page_tags", "page_meta", "page_properties", "search_pages"}
var dryRunRollback = errors.New("dry-run rollback")

func CheckPending(dataDir string) error {
	if _, err := os.Lstat(filepath.Join(dataDir, PendingFile)); err == nil {
		return errors.New("unfinished data transfer: keep the application stopped and inspect the transfer journal before recovery")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ImportLegacy requires a stopped source and stopped target application. DryRun
// exercises the same SQL and constraints but always rolls back and copies no files.
// A failed COMMIT is explicitly indeterminate: retain files/journal and refuse
// startup/retry rather than claiming rollback and deleting possibly committed data.
func ImportLegacy(ctx context.Context, pg *postgres.Store, source, target string, dryRun bool) (Report, error) {
	report, err := InspectLegacy(ctx, source)
	if err != nil {
		return report, err
	}
	if !report.Valid() {
		return report, errors.New("legacy preflight failed; nothing committed")
	}
	if err := pg.CheckSchema(ctx); err != nil {
		return report, err
	}
	if err := CheckPending(target); err != nil {
		return report, err
	}
	if _, err := os.Lstat(filepath.Join(target, receiptFile)); err == nil {
		return report, errors.New("target already has an import receipt; duplicate import refused")
	} else if !os.IsNotExist(err) {
		return report, err
	}
	sourceAbs, err := resolvedPath(source)
	if err != nil {
		return report, err
	}
	targetAbs, err := resolvedPath(target)
	if err != nil {
		return report, err
	}
	if targetAbs == sourceAbs || strings.HasPrefix(targetAbs, sourceAbs+string(os.PathSeparator)) || strings.HasPrefix(sourceAbs, targetAbs+string(os.PathSeparator)) {
		return report, errors.New("source and target must be separate directories")
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		return report, errors.New("legacy import requires an empty target directory")
	} else if err != nil && !os.IsNotExist(err) {
		return report, err
	}
	files, err := Inventory(ctx, source)
	if err != nil {
		return report, err
	}
	workReport := Report{Counts: map[string]int{}}
	pages, err := inspectPages(ctx, source, &workReport)
	if err != nil {
		return report, err
	}
	revisions, err := inspectRevisions(ctx, source, files, pages, &workReport)
	if err != nil {
		return report, err
	}
	auth, err := readAuth(ctx, source, &workReport)
	if err != nil {
		return report, err
	}
	report.Issues = append(report.Issues, workReport.Issues...)
	if !report.Valid() {
		return report, errors.New("legacy import compatibility checks failed; nothing committed")
	}
	after, err := Inventory(ctx, source)
	if err != nil {
		return report, err
	}
	if inventoryHash(after) != report.SourceHash {
		return report, errors.New("source changed; nothing committed")
	}
	installed := []string{}
	readyToCommit := false
	journalCreated := false
	err = pg.WithTx(ctx, func(db postgres.DBTX) error {
		if err := lockAndCheckEmpty(ctx, db); err != nil {
			return err
		}
		for _, p := range pages {
			metadata, err := json.Marshal(p.Node.Metadata)
			if err != nil {
				return err
			}
			var parent any
			if p.Node.Parent != nil {
				parent = p.Node.Parent.ID
			}
			if p.Node.ID == "root" {
				_, err = db.Exec(ctx, `UPDATE pages SET metadata=$1 WHERE id='root'`, metadata)
			} else {
				_, err = db.Exec(ctx, `INSERT INTO pages(id,parent_id,title,slug,kind,position,pinned,metadata,content_markdown) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.Node.ID, parent, p.Node.Title, p.Node.Slug, string(p.Node.Kind), p.Node.Position, p.Node.Pinned, metadata, p.Raw)
			}
			if err != nil {
				return errors.New("page import constraint/write failed; transaction aborted")
			}
		}
		for _, r := range revisions {
			for _, statement := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO revision_contents(page_id,content_hash,content_markdown) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, []any{r.Revision.PageID, r.Revision.ContentHash, r.Content}},
				{`INSERT INTO revision_asset_manifests(hash,manifest) VALUES($1,$2) ON CONFLICT DO NOTHING`, []any{r.Revision.AssetManifestHash, r.Manifest}},
				{`INSERT INTO revisions(page_id,id,sort_key,content_hash,asset_manifest_hash,metadata) VALUES($1,$2,$3,$4,$5,$6)`, []any{r.Revision.PageID, r.Revision.ID, r.SortKey, r.Revision.ContentHash, r.Revision.AssetManifestHash, r.Raw}},
			} {
				if _, err := db.Exec(ctx, statement.sql, statement.args...); err != nil {
					return errors.New("revision import constraint/write failed; transaction aborted")
				}
			}
		}
		if _, err := db.Exec(ctx, `UPDATE pages SET current_revision_id=(SELECT id FROM revisions WHERE page_id=pages.id ORDER BY sort_key DESC LIMIT 1)`); err != nil {
			return err
		}
		for _, table := range auth {
			placeholders := make([]string, len(table.Columns))
			for i := range placeholders {
				placeholders[i] = fmt.Sprintf("$%d", i+1)
			}
			query := "INSERT INTO " + table.Name + " (" + strings.Join(table.Columns, ",") + ") VALUES (" + strings.Join(placeholders, ",") + ")"
			for _, row := range table.Rows {
				if _, err := db.Exec(ctx, query, row...); err != nil {
					return fmt.Errorf("%s import constraint/write failed; transaction aborted", table.Name)
				}
			}
		}
		for _, setting := range []struct{ file, key string }{{"branding.json", "branding"}, {"public-access.json", "public_access"}} {
			raw, err := os.ReadFile(filepath.Join(source, setting.file))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if _, err := db.Exec(ctx, `INSERT INTO instance_settings(key,value) VALUES($1,$2)`, setting.key, json.RawMessage(raw)); err != nil {
				return errors.New("settings import failed; transaction aborted")
			}
		}
		// Check deferred current-revision foreign keys during dry-run too.
		if _, err := db.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
			return err
		}
		if dryRun {
			return dryRunRollback
		}
		if err := os.MkdirAll(target, 0700); err != nil {
			return err
		}
		journal, _ := json.Marshal(map[string]any{"operation": "legacy-import", "source_hash": report.SourceHash, "state": "commit-not-confirmed"})
		if err := writeExclusive(filepath.Join(target, PendingFile), journal); err != nil {
			return err
		}
		journalCreated = true
		for _, file := range files {
			if !persistentFile(file.Path) {
				continue
			}
			dst := filepath.Join(target, filepath.FromSlash(file.Path))
			if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
				return err
			}
			installed = append(installed, dst)
			if err := copyRegular(ctx, filepath.Join(source, filepath.FromSlash(file.Path)), dst); err != nil {
				return err
			}
		}
		final, err := Inventory(ctx, source)
		if err != nil {
			return err
		}
		if inventoryHash(final) != report.SourceHash {
			return errors.New("source changed while copying; transaction aborted")
		}
		readyToCommit = true
		return nil
	})
	if errors.Is(err, dryRunRollback) {
		return report, nil
	}
	if err != nil {
		if readyToCommit {
			report.CommitUnknown = true
			return report, errors.New("import COMMIT outcome unknown; target files and transfer journal retained; do not start or retry until the database outcome is checked")
		}
		if journalCreated {
			for _, path := range installed {
				if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
					return report, fmt.Errorf("import rolled back but file cleanup failed; transfer journal retained")
				}
			}
			// Target was empty before this operation. Remove directories created by us.
			for _, name := range []string{"assets", "avatars", "branding", ".leafwiki", "root"} {
				if e := os.RemoveAll(filepath.Join(target, name)); e != nil {
					return report, errors.New("import rolled back but cleanup failed; journal retained")
				}
			}
			if e := os.Remove(filepath.Join(target, PendingFile)); e != nil {
				return report, e
			}
		}
		return report, err
	}
	report.Committed = true
	receipt, _ := json.Marshal(map[string]any{"source_hash": report.SourceHash, "counts": report.Counts})
	if err := writeExclusive(filepath.Join(target, receiptFile), receipt); err != nil {
		return report, errors.New("canonical import committed; receipt failed; journal retained")
	}
	if err := RebuildDerived(ctx, pg, target); err != nil {
		return report, fmt.Errorf("canonical import committed; derived rebuild failed; journal retained: %w", err)
	}
	if err := os.Remove(filepath.Join(target, PendingFile)); err != nil {
		return report, err
	}
	return report, nil
}

func persistentFile(path string) bool {
	return strings.HasPrefix(path, "assets/") || strings.HasPrefix(path, "avatars/") || strings.HasPrefix(path, "branding/") || strings.HasPrefix(path, ".leafwiki/blobs/assets/") || isIgnoreFile(path)
}

func isIgnoreFile(path string) bool {
	return strings.HasPrefix(path, "root/") && strings.HasSuffix(path, "/.leafwikiignore")
}
func writeExclusive(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// Resolve existing ancestors too, so a target reached through a symlink cannot
// alias the read-only source even when the final target directory is new.
func resolvedPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(abs)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", err
		}
		suffix = append(suffix, filepath.Base(abs))
		abs = parent
	}
}
func lockAndCheckEmpty(ctx context.Context, db postgres.DBTX) error {
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, transferLockID); err != nil {
		return err
	}
	tables := append(append([]string{}, canonicalTables...), derivedTables...)
	if _, err := db.Exec(ctx, "LOCK TABLE "+strings.Join(tables, ",")+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return err
	}
	for _, table := range tables {
		query := "SELECT count(*) FROM " + table
		if table == "pages" {
			query += " WHERE id<>'root' OR content_markdown IS NOT NULL OR current_revision_id IS NOT NULL"
		}
		var count int64
		if err := db.QueryRow(ctx, query).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("target database is not empty; import refused")
		}
	}
	return nil
}
