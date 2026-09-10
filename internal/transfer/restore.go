package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/perber/wiki/internal/storage/postgres"
)

const transferLockID int64 = 0x4c575452414e5346

type RestoreOptions struct {
	Replace      bool
	MaxBytes     int64
	AfterRestore func() error
}

// RestoreBackup consumes trusted, complete LeafWiki PostgreSQL backup artifacts.
// The caller must stop writers or hold the runtime maintenance gate. Database
// replacement is pg_restore's single transaction; old files and a durable journal
// remain on any indeterminate outcome. We never claim SQL+filesystem atomicity.
func RestoreBackup(ctx context.Context, pg *postgres.Store, dsn, dataDir, archive string, tools PGTools, options RestoreOptions) error {
	if err := CheckPending(dataDir); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	stage, manifest, err := StageBackup(ctx, archive, dataDir, options.MaxBytes)
	if err != nil {
		return err
	}
	retain := false
	defer func() {
		if !retain {
			os.RemoveAll(stage)
		}
	}()
	if err := pg.CheckSchema(ctx); err != nil {
		return err
	}
	status, err := pg.Status(ctx)
	if err != nil {
		return err
	}
	if manifest.SchemaVersion != status.Migrations[len(status.Migrations)-1].Version || manifest.PGroongaVersion != status.PGroongaVersion {
		return errors.New("backup schema/PGroonga version differs from this target; restore refused")
	}
	fingerprint, err := migrationFingerprint(ctx, pg.DB())
	if err != nil {
		return err
	}
	if fingerprint != manifest.SchemaChecksum {
		return errors.New("backup migration checksums differ from target; restore refused")
	}
	dumpPath := filepath.Join(stage, "database.dump")
	dump, err := os.Open(dumpPath)
	if err != nil {
		return err
	}
	err = tools.run(ctx, true, "", dump, io.Discard, "--list")
	dump.Close()
	if err != nil {
		return err
	}
	old := filepath.Join(stage, "previous-files")
	names := append(append([]string{}, assetRoots...), receiptFile)
	ignoreNames := map[string]bool{}
	for _, file := range manifest.Files {
		if isIgnoreFile(file.Path) {
			ignoreNames[file.Path] = true
		}
	}
	currentFiles, err := persistentInventory(ctx, dataDir)
	if err != nil {
		return err
	}
	for _, file := range currentFiles {
		if isIgnoreFile(file.Path) {
			ignoreNames[file.Path] = true
		}
	}
	for name := range ignoreNames {
		names = append(names, name)
	}
	if !options.Replace {
		for _, name := range names {
			if _, err := os.Lstat(filepath.Join(dataDir, name)); err == nil {
				return errors.New("target has persistent files; explicit replacement required")
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	err = pg.WithTx(ctx, func(lockDB postgres.DBTX) error {
		if _, err := lockDB.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, transferLockID); err != nil {
			return err
		}
		if !options.Replace {
			if err := checkEmpty(ctx, pg.DB()); err != nil {
				return err
			}
		}
		journal, _ := json.Marshal(map[string]any{"operation": "postgres-restore", "state": "outcome-not-confirmed", "staging_directory": stage, "previous_files": old})
		if err := writeExclusive(filepath.Join(dataDir, PendingFile), journal); err != nil {
			return err
		}
		retain = true
		// Each rename is on the target filesystem. On failure, retain both the
		// journal and old files, including the exact per-component swap progress.
		for _, name := range names {
			current := filepath.Join(dataDir, name)
			previous := filepath.Join(old, name)
			incoming := filepath.Join(stage, name)
			if _, err := os.Lstat(current); err == nil {
				if err := os.MkdirAll(filepath.Dir(previous), 0700); err != nil {
					return err
				}
				if err := os.Rename(current, previous); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if _, err := os.Lstat(incoming); err == nil {
				if err := os.MkdirAll(filepath.Dir(current), 0700); err != nil {
					return err
				}
				if err := os.Rename(incoming, current); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		in, err := os.Open(dumpPath)
		if err != nil {
			return err
		}
		defer in.Close()
		return tools.run(ctx, true, dsn, in, io.Discard, "--single-transaction", "--clean", "--if-exists", "--no-owner", "--no-acl", "--exit-on-error")
	})
	if err != nil {
		if retain {
			return fmt.Errorf("restore did not complete; database outcome must be checked, prior files and transfer journal retained; keep the application stopped: %w", err)
		}
		return err
	}
	pg.ResetConnections()
	if err := pg.CheckSchema(ctx); err != nil {
		return fmt.Errorf("restore database applied but schema verification failed; journal retained: %w", err)
	}
	if err := RebuildDerived(ctx, pg, dataDir); err != nil {
		return fmt.Errorf("restore database applied but derived rebuild failed; journal retained: %w", err)
	}
	if options.AfterRestore != nil {
		if err := options.AfterRestore(); err != nil {
			return fmt.Errorf("restore data applied but runtime reload failed; journal retained: %w", err)
		}
	}
	if err := os.Remove(filepath.Join(dataDir, PendingFile)); err != nil {
		return err
	}
	retain = false
	return nil
}

func checkEmpty(ctx context.Context, db postgres.DBTX) error {
	tables := append(append([]string{}, canonicalTables...), derivedTables...)
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
			return errors.New("target database is not empty; explicit replacement required")
		}
	}
	return nil
}
