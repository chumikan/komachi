package transfer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/favorites"
	"github.com/perber/wiki/internal/search"
	"github.com/perber/wiki/internal/test_utils"
	"golang.org/x/crypto/bcrypt"
)

func TestPostgresLegacyImport(t *testing.T) {
	ctx := context.Background()
	pg, cfg := test_utils.PostgresStore(t)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	source, rev := revisionFixture(t)
	target := filepath.Join(t.TempDir(), "target")
	us, err := auth.NewUserStore(source)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("existing-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := us.CreateUser(&auth.User{ID: "original-user", Username: "original", Email: "original@example.org", Role: auth.RoleAdmin, Password: string(hash)}); err != nil {
		t.Fatal(err)
	}
	us.Close()
	fs, err := favorites.NewFavoritesStore(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Add("original-user", "page-id"); err != nil {
		t.Fatal(err)
	}
	fs.Close()
	legacySQL, err := sql.Open("sqlite", filepath.Join(source, "favorites.db"))
	if err != nil {
		t.Fatal(err)
	}
	timestamp := "2025-01-01 00:00:00.123456789 +0000 UTC"
	if _, err := legacySQL.Exec(`UPDATE favorites SET created_at=$1`, timestamp); err != nil {
		t.Fatal(err)
	}
	legacySQL.Close()
	writeFixture(t, source, "assets/page-id/file.txt", "asset bytes")
	before, err := Inventory(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ImportLegacy(ctx, pg, source, target, true)
	if err != nil || !r.Valid() || r.Committed {
		t.Fatalf("dry-run: %+v %v", r, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("dry-run created target")
	}
	var count int
	if err := pg.DB().QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil || count != 0 {
		t.Fatal("dry-run committed")
	}
	r, err = ImportLegacy(ctx, pg, source, target, false)
	if err != nil || !r.Committed {
		t.Fatalf("import: %+v %v", r, err)
	}
	if err := CheckPending(target); err != nil {
		t.Fatal(err)
	}
	treeService := tree.NewPostgresTreeService(target, pg)
	if err := treeService.LoadTree(); err != nil {
		t.Fatal(err)
	}
	page, err := treeService.GetPage("page-id")
	if err != nil {
		t.Fatal(err)
	}
	if page.RawContent != pageFixture("page-id", "ページ") {
		t.Fatal("raw Markdown changed")
	}
	var revisionID, password, original string
	var remainder int
	if err := pg.DB().QueryRow(ctx, `SELECT current_revision_id FROM pages WHERE id='page-id'`).Scan(&revisionID); err != nil || revisionID != rev.ID {
		t.Fatal("revision identity changed")
	}
	if err := pg.DB().QueryRow(ctx, `SELECT password FROM users WHERE id='original-user'`).Scan(&password); err != nil || password != string(hash) {
		t.Fatal("password changed")
	}
	if err := pg.DB().QueryRow(ctx, `SELECT created_at_legacy,created_at_submicro FROM favorites`).Scan(&original, &remainder); err != nil || original != timestamp || remainder != 789 {
		t.Fatalf("timestamp loss: %v %d %v", original, remainder, err)
	}
	index, err := search.NewPostgreSQLIndex(pg)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	results, err := index.Search("本文", nil, 0, 10)
	if err != nil || results.Count != 1 {
		t.Fatalf("rebuilt search: %+v %v", results, err)
	}
	if r, err := ImportLegacy(ctx, pg, source, target, false); err == nil || r.Committed {
		t.Fatal("duplicate import accepted")
	}
	after, err := Inventory(ctx, source)
	if err != nil || inventoryHash(before) != inventoryHash(after) {
		t.Fatal("source changed")
	}
	_ = cfg
}

func TestPostgresImportRollback(t *testing.T) {
	ctx := context.Background()
	pg, _ := test_utils.PostgresStore(t)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	source, _ := revisionFixture(t)
	// Trigger a failure after pages have been inserted, before revisions finish.
	if _, err := pg.DB().Exec(ctx, `CREATE FUNCTION reject_import_revision() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'injected'; END$$; CREATE TRIGGER reject_import_revision BEFORE INSERT ON revisions FOR EACH ROW EXECUTE FUNCTION reject_import_revision()`); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	r, err := ImportLegacy(ctx, pg, source, target, false)
	if err == nil || r.Committed {
		t.Fatal("failed transaction reported success")
	}
	var count int
	if err := pg.DB().QueryRow(ctx, `SELECT count(*) FROM pages WHERE id<>'root'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("page survived revision failure")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("failed import created files")
	}
}
