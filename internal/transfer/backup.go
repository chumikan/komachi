package transfer

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/perber/wiki/internal/storage/postgres"
)

const BackupFormat = "leafwiki-postgresql"
const BackupFormatVersion = 1
const manifestName = "postgres-manifest.json"

var assetRoots = []string{"assets", "avatars", "branding", ".leafwiki/blobs/assets"}

type BackupManifest struct {
	Format             string       `json:"format"`
	FormatVersion      int          `json:"format_version"`
	SchemaVersion      int          `json:"schema_version"`
	SchemaChecksum     string       `json:"schema_checksum"`
	CreatedAt          time.Time    `json:"created_at"`
	ApplicationVersion string       `json:"application_version"`
	PostgreSQLVersion  string       `json:"postgresql_version"`
	PGroongaVersion    string       `json:"pgroonga_version"`
	Files              []FileRecord `json:"files"`
}

// CreateBackup requires the application write gate to be held and drained (or
// the application stopped). pg_dump alone cannot snapshot filesystem assets.
func CreateBackup(ctx context.Context, pg *postgres.Store, dsn, dataDir, destination, id, version string, tools PGTools) error {
	if err := CheckPending(dataDir); err != nil {
		return err
	}
	if err := pg.CheckSchema(ctx); err != nil {
		return err
	}
	status, err := pg.Status(ctx)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp("", "leafwiki-pg-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	dump, err := os.OpenFile(filepath.Join(stage, "database.dump"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = tools.run(ctx, false, dsn, nil, dump, "--format=custom", "--no-owner", "--no-acl")
	closeErr := dump.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, root := range assetRoots {
		if err := os.MkdirAll(filepath.Join(stage, root), 0700); err != nil {
			return err
		}
	}
	// Inventory only persistent components: do not traverse the backup directory
	// or legacy indexes, which are not canonical PostgreSQL runtime data.
	sourceRecords, err := persistentInventory(ctx, dataDir)
	if err != nil {
		return err
	}
	for _, file := range sourceRecords {
		target := filepath.Join(stage, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := copyRegular(ctx, filepath.Join(dataDir, filepath.FromSlash(file.Path)), target); err != nil {
			return err
		}
	}
	after, err := persistentInventory(ctx, dataDir)
	if err != nil {
		return err
	}
	if inventoryHash(after) != inventoryHash(sourceRecords) {
		return errors.New("assets changed during backup; no complete backup published")
	}
	now := time.Now().UTC()
	meta, _ := json.Marshal(map[string]any{"id": id, "createdAt": now, "version": version})
	if err := writeExclusive(filepath.Join(stage, "backup-meta.json"), meta); err != nil {
		return err
	}
	files, err := Inventory(ctx, stage)
	if err != nil {
		return err
	}
	manifest := BackupManifest{Format: BackupFormat, FormatVersion: BackupFormatVersion, SchemaVersion: status.Migrations[len(status.Migrations)-1].Version, CreatedAt: now, ApplicationVersion: version, PostgreSQLVersion: status.PostgreSQLVersion, PGroongaVersion: status.PGroongaVersion, Files: files}
	manifest.SchemaChecksum, err = migrationFingerprint(ctx, pg.DB())
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeExclusive(filepath.Join(stage, manifestName), raw); err != nil {
		return err
	}
	// Publish by rename only after a closed and verified artifact exists.
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	out, err := os.CreateTemp(parent, ".leafwiki-backup-*.zip")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	zw := zip.NewWriter(out)
	for _, root := range assetRoots {
		h := &zip.FileHeader{Name: root + "/", Method: zip.Store}
		h.SetMode(os.ModeDir | 0700)
		if _, err := zw.CreateHeader(h); err != nil {
			zw.Close()
			out.Close()
			return err
		}
	}
	for _, name := range append(recordPaths(files), manifestName) {
		if err := ctx.Err(); err != nil {
			zw.Close()
			out.Close()
			return err
		}
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0600)
		entry, err := zw.CreateHeader(h)
		if err != nil {
			zw.Close()
			out.Close()
			return err
		}
		in, err := os.Open(filepath.Join(stage, filepath.FromSlash(name)))
		if err != nil {
			zw.Close()
			out.Close()
			return err
		}
		_, copyErr := io.Copy(entry, contextReader{ctx: ctx, reader: in})
		in.Close()
		if copyErr != nil {
			zw.Close()
			out.Close()
			return copyErr
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	verified, _, err := StageBackup(ctx, tmp, parent, 0)
	if err != nil {
		return err
	}
	os.RemoveAll(verified)
	// Link is an atomic no-overwrite publication on the same filesystem.
	if err := os.Link(tmp, destination); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func recordPaths(files []FileRecord) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}
func persistentInventory(ctx context.Context, dataDir string) ([]FileRecord, error) {
	var result []FileRecord
	for _, root := range assetRoots {
		path := filepath.Join(dataDir, root)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		files, err := Inventory(ctx, path)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			f.Path = root + "/" + f.Path
			result = append(result, f)
		}
	}
	for _, name := range []string{"git-backup.json", receiptFile} {
		path := filepath.Join(dataDir, name)
		st, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, errors.New("persistent file is not regular")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		result = append(result, FileRecord{Path: name, SHA256: hashBytes(raw), Size: int64(len(raw)), ModifiedAt: st.ModTime().UTC()})
	}
	root := filepath.Join(dataDir, "root")
	if _, err := os.Lstat(root); err == nil {
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("symlink in legacy root may hide persistent ignore configuration")
			}
			if entry.Name() != ".leafwikiignore" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("ignore configuration is not a regular file")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dataDir, path)
			result = append(result, FileRecord{Path: filepath.ToSlash(rel), SHA256: hashBytes(raw), Size: int64(len(raw)), ModifiedAt: info.ModTime().UTC()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

// StageBackup validates every component before any database or live file change.
// Checksums establish completeness, NOT origin/authenticity. Input must be trusted.
// maxBytes=0 is for operator-selected local backups; uploads set an explicit cap.
func StageBackup(ctx context.Context, archive, parent string, maxBytes int64) (string, BackupManifest, error) {
	var manifest BackupManifest
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return "", manifest, err
	}
	defer zr.Close()
	entries := map[string]*zip.File{}
	var total uint64
	for _, file := range zr.File {
		name := file.Name
		if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || strings.Contains(name, ":") || filepath.ToSlash(filepath.Clean(name)) != strings.TrimSuffix(name, "/") {
			return "", manifest, errors.New("unsafe backup path")
		}
		if _, ok := entries[name]; ok {
			return "", manifest, errors.New("duplicate backup path")
		}
		entries[name] = file
		if file.Mode()&os.ModeSymlink != 0 || (!file.FileInfo().IsDir() && !file.Mode().IsRegular()) {
			return "", manifest, errors.New("unsupported backup file type")
		}
		previous := total
		total += file.UncompressedSize64
		if total < previous || (maxBytes > 0 && total > uint64(maxBytes)) {
			return "", manifest, errors.New("backup exceeds extraction limit")
		}
	}
	mf, ok := entries[manifestName]
	if !ok || mf.UncompressedSize64 > 16*1024*1024 {
		return "", manifest, errors.New("missing/oversized PostgreSQL backup manifest")
	}
	reader, err := mf.Open()
	if err != nil {
		return "", manifest, err
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 16*1024*1024+1))
	reader.Close()
	if err != nil || len(raw) > 16*1024*1024 {
		return "", manifest, errors.New("unreadable backup manifest")
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Format != BackupFormat || manifest.FormatVersion != BackupFormatVersion || manifest.SchemaVersion <= 0 || !validHash(manifest.SchemaChecksum) || manifest.CreatedAt.IsZero() {
		return "", manifest, errors.New("unsupported PostgreSQL backup format")
	}
	expected := map[string]bool{manifestName: true}
	for _, root := range assetRoots {
		expected[root+"/"] = true
		if entries[root+"/"] == nil {
			return "", manifest, errors.New("missing asset archive component")
		}
	}
	for _, file := range manifest.Files {
		if expected[file.Path] || !validHash(file.SHA256) || file.Size < 0 {
			return "", manifest, errors.New("invalid or duplicate manifest component")
		}
		if file.Path != "database.dump" && file.Path != "backup-meta.json" && file.Path != receiptFile && !persistentFile(file.Path) {
			return "", manifest, errors.New("unrecognized backup component")
		}
		entry := entries[file.Path]
		if entry == nil || entry.FileInfo().IsDir() || entry.UncompressedSize64 != uint64(file.Size) {
			return "", manifest, errors.New("missing component or size mismatch")
		}
		expected[file.Path] = true
	}
	if !expected["database.dump"] || !expected["backup-meta.json"] || len(expected) != len(entries) {
		return "", manifest, errors.New("incomplete or unexpected backup components")
	}
	stage, err := os.MkdirTemp(parent, ".leafwiki-restore-stage-*")
	if err != nil {
		return "", manifest, err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(stage)
		}
	}()
	for _, root := range assetRoots {
		if err := os.MkdirAll(filepath.Join(stage, root), 0700); err != nil {
			return "", manifest, err
		}
	}
	for _, file := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return "", manifest, err
		}
		dst := filepath.Join(stage, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return "", manifest, err
		}
		in, err := entries[file.Path].Open()
		if err != nil {
			return "", manifest, err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			in.Close()
			return "", manifest, err
		}
		n, copyErr := io.Copy(out, contextReader{ctx: ctx, reader: io.LimitReader(in, file.Size+1)})
		in.Close()
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || n != file.Size {
			return "", manifest, errors.New("backup component read failed")
		}
		if file.ModifiedAt.IsZero() {
			return "", manifest, errors.New("backup component timestamp missing")
		}
		if err := os.Chtimes(dst, file.ModifiedAt, file.ModifiedAt); err != nil {
			return "", manifest, err
		}
	}
	actual, err := Inventory(ctx, stage)
	if err != nil {
		return "", manifest, err
	}
	actualMap := map[string]FileRecord{}
	for _, file := range actual {
		actualMap[file.Path] = file
	}
	for _, file := range manifest.Files {
		if actualMap[file.Path] != file {
			return "", manifest, fmt.Errorf("backup component checksum mismatch: %s", file.Path)
		}
	}
	header, err := os.Open(filepath.Join(stage, "database.dump"))
	if err != nil {
		return "", manifest, err
	}
	magic := make([]byte, 5)
	_, err = io.ReadFull(header, magic)
	header.Close()
	if err != nil || string(magic) != "PGDMP" {
		return "", manifest, errors.New("invalid PostgreSQL custom archive")
	}
	success = true
	return stage, manifest, nil
}

func migrationFingerprint(ctx context.Context, db postgres.DBTX) (string, error) {
	var rows string
	if err := db.QueryRow(ctx, `SELECT coalesce(string_agg(version::text || ':' || name || ':' || checksum, E'\n' ORDER BY version),'') FROM schema_migrations`).Scan(&rows); err != nil {
		return "", err
	}
	return hashBytes([]byte(rows)), nil
}
