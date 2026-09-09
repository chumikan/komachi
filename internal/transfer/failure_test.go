package transfer

import (
	"context"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/perber/wiki/internal/test_utils"
)

func TestPostgresInterruptedImportLeavesNoPartialRows(t *testing.T) {
	pg, _ := test_utils.PostgresStore(t)
	ctx := context.Background()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	source, _ := revisionFixture(t)
	if _, err := pg.DB().Exec(ctx, `CREATE FUNCTION slow_import_revision() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_sleep(5); RETURN NEW; END$$; CREATE TRIGGER slow_import_revision BEFORE INSERT ON revisions FOR EACH ROW EXECUTE FUNCTION slow_import_revision()`); err != nil {
		t.Fatal(err)
	}
	interrupted, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	target := filepath.Join(t.TempDir(), "target")
	r, err := ImportLegacy(interrupted, pg, source, target, false)
	if err == nil || r.Committed || r.CommitUnknown {
		t.Fatal("interrupted pre-commit import did not report rollback")
	}
	var count int
	if err := pg.DB().QueryRow(ctx, `SELECT count(*) FROM pages WHERE id<>'root'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("interrupted import retained pages")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("interrupted pre-copy import left files")
	}
}

func TestPostgresImportAfterConnectionLoss(t *testing.T) {
	ctx := context.Background()
	pg, cfg := test_utils.PostgresStore(t)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := pg.DB().QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	var terminated bool
	if err := admin.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatal("failed to simulate lost connection")
	}
	source, _ := revisionFixture(t)
	target := filepath.Join(t.TempDir(), "target")
	r, err := ImportLegacy(ctx, pg, source, target, false)
	if err != nil && !r.Committed && !r.CommitUnknown {
		r, err = ImportLegacy(ctx, pg, source, target, false)
	}
	if err != nil || !r.Committed {
		t.Fatalf("pool did not recover for import: %v", err)
	}
}

func TestPostgresRestoreRetainsJournalOnLostCompletion(t *testing.T) {
	ctx := context.Background()
	pg, cfg := test_utils.PostgresStore(t)
	tools := integrationPGTools(t)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	source, _ := revisionFixture(t)
	dir := filepath.Join(t.TempDir(), "imported")
	if _, err := ImportLegacy(ctx, pg, source, dir, false); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if err := CreateBackup(ctx, pg, cfg.URL, dir, archive, "snapshot-failure", "test", tools); err != nil {
		t.Fatal(err)
	}
	target, targetCfg := test_utils.PostgresStore(t)
	if err := target.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	realRestore := tools.Restore
	if realRestore == "" {
		realRestore = "pg_restore"
	}
	wrapper := filepath.Join(t.TempDir(), "restore-lost-response")
	script := "#!/bin/sh\n" + "'" + strings.ReplaceAll(realRestore, "'", "'\\''") + "' \"$@\" || exit $?\nfor arg in \"$@\"; do if [ \"$arg\" = --list ]; then exit 0; fi; done\nexit 23\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	tools.Restore = wrapper
	targetDir := t.TempDir()
	if err := RestoreBackup(ctx, target, targetCfg.URL, targetDir, archive, tools, RestoreOptions{}); err == nil {
		t.Fatal("lost completion reported success")
	}
	if err := CheckPending(targetDir); err == nil {
		t.Fatal("indeterminate restore permits startup")
	}
	target.ResetConnections()
	var count int
	if err := target.DB().QueryRow(ctx, `SELECT count(*) FROM pages WHERE id='page-id'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("fixture must have committed before losing response")
	}
	if err := RestoreBackup(ctx, target, targetCfg.URL, targetDir, archive, tools, RestoreOptions{Replace: true}); err == nil {
		t.Fatal("retry over unfinished restore accepted")
	}
}

func TestPostgresInterruptedBackupIsNotPublished(t *testing.T) {
	pg, cfg := test_utils.PostgresStore(t)
	ctx := context.Background()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(t.TempDir(), "interrupted-dump")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf PGDMPpartial\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if err := CreateBackup(ctx, pg, cfg.URL, t.TempDir(), archive, "snapshot-failure", "test", PGTools{Dump: command}); err == nil {
		t.Fatal("interrupted dump reported success")
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("partial dump published as backup")
	}
}

func TestImportRejectsSourceAlias(t *testing.T) {
	source := t.TempDir()
	parent := t.TempDir()
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Skip(err)
	}
	a, err := resolvedPath(source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := resolvedPath(alias)
	if err != nil || a != b {
		t.Fatal("source alias escaped detection")
	}
}
