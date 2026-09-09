package wiki

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/perber/wiki/internal/test_utils"
	"github.com/perber/wiki/internal/transfer"
)

// Restore must be usable by the real application composition root, not just SQL readers.
func TestPostgresWikiBootAfterCompleteRestore(t *testing.T) {
	ctx := context.Background()
	db, config := test_utils.PostgresStore(t)
	tools := transfer.PGTools{Dump: os.Getenv("LEAFWIKI_TEST_PG_DUMP"), Restore: os.Getenv("LEAFWIKI_TEST_PG_RESTORE")}
	for _, command := range []struct{ override, fallback string }{{tools.Dump, "pg_dump"}, {tools.Restore, "pg_restore"}} {
		name := command.override
		if name == "" {
			name = command.fallback
		}
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("PostgreSQL client unavailable")
		}
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	observeNoSQLite(t, dir)
	w, err := NewWiki(&WikiOptions{Postgres: db, StorageDir: dir, AuthDisabled: true, EnableRevision: true, RevisionCoalesceWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	page := createPageForTest(t, w, "system", nil, "復元後の起動", "restored-boot", pageNodeKind())
	want, err := w.tree.GetPage(page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "complete.zip")
	if err := transfer.CreateBackup(ctx, db, config.URL, dir, archive, "boot-test", "test", tools); err != nil {
		t.Fatal(err)
	}
	target, targetConfig := test_utils.PostgresStore(t)
	if err := target.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	restoredDir := t.TempDir()
	observeNoSQLite(t, restoredDir)
	if err := transfer.RestoreBackup(ctx, target, targetConfig.URL, restoredDir, archive, tools, transfer.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		restored, err := NewWiki(&WikiOptions{Postgres: target, StorageDir: restoredDir, AuthDisabled: true, EnableRevision: true})
		if err != nil {
			t.Fatal(err)
		}
		// Startup rebuild is asynchronous; search is ready after its worker drains.
		restored.reloadWG.Wait()
		got, err := restored.tree.GetPage(page.ID)
		if err != nil || got.RawContent != want.RawContent || got.Title != want.Title || got.Slug != want.Slug {
			t.Fatal("application restart changed restored page")
		}
		result, err := restored.searchIndex.Search("復元後", nil, 0, 10)
		if err != nil || result.Count != 1 || result.Items[0].PageID != page.ID {
			t.Fatal("restored application search failed")
		}
		if err := restored.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
