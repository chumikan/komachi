package wiki

import (
	"context"
	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/core/email"
	"github.com/perber/wiki/internal/usersettings"
	wikipages "github.com/perber/wiki/internal/wiki/pages"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/perber/wiki/internal/test_utils"
)

// Exercise the real composition root and all existing derived side effects.
// Canonical and derived stores must all use PostgreSQL in normal runtime.
func TestPostgresWikiLifecycle(t *testing.T) {
	db, _ := test_utils.PostgresStore(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	opts := &WikiOptions{Postgres: db, StorageDir: t.TempDir(), AuthDisabled: true, EnableRevision: true, RevisionCoalesceWindow: time.Hour}
	observeNoSQLite(t, opts.StorageDir)
	w, err := NewWiki(opts)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			if err := w.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	section := createPageForTest(t, w, "system", nil, "Section", "section", pageNodeKind())
	page := createPageForTest(t, w, "system", &section.ID, "日本語ページ", "japanese", pageNodeKind())
	body := "---\ntags: [東京, Go]\nproject: LeafWiki\n---\n日本語本文 [[Section]]"
	if _, err := wikipages.NewUpdatePageUseCase(w.tree, w.slug, w.newPageOrchestrator(), w.log, nil).Execute(context.Background(), wikipages.UpdatePageInput{UserID: "system", ID: page.ID, Version: page.Version(), Title: page.Title, Slug: page.Slug, Content: &body, Kind: pageNodeKind(), PreserveFrontmatter: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.ReloadFromFS(); err != nil {
		t.Fatal(err)
	}
	result, err := w.searchIndex.Search("日本語", nil, 0, 10)
	if err != nil || result.Count != 1 || result.Items[0].PageID != page.ID {
		t.Fatalf("runtime Japanese search: %+v %v", result, err)
	}
	tagIDs, err := w.tags.GetPageIDsByTags([]string{"東京", "go"})
	if err != nil || len(tagIDs) != 1 {
		t.Fatalf("runtime tags: %v %v", tagIDs, err)
	}
	propIDs, err := w.props.GetPageIDsByProperty("project", "LeafWiki")
	if err != nil || len(propIDs) != 1 {
		t.Fatalf("runtime properties: %v %v", propIDs, err)
	}
	backlinks, err := w.links.GetBacklinksForPage(section.ID)
	if err != nil || len(backlinks.Backlinks) != 1 {
		t.Fatalf("runtime backlinks: %+v %v", backlinks, err)
	}
	current, err := w.tree.GetPage(page.ID)
	if err != nil || current.RawContent == "" || current.Content != "日本語本文 [[Section]]" {
		t.Fatalf("rescan lost PostgreSQL page: %+v %v", current, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	w, err = NewWiki(opts)
	if err != nil {
		t.Fatal(err)
	}
	closed = false
	persisted, err := w.tree.GetPage(page.ID)
	if err != nil || persisted.Content != "日本語本文 [[Section]]" {
		t.Fatalf("wiki restart: %+v %v", persisted, err)
	}
	history, err := w.revision.ListRevisions(page.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("restart history: %d %v", len(history), err)
	}
	deletePageForTest(t, w, "system", section.ID, true)
	if _, err := w.tree.GetPage(page.ID); err == nil {
		t.Fatal("recursive deletion left child")
	}
	history, err = w.revision.ListRevisions(page.ID)
	if err != nil || len(history) != 0 {
		t.Fatalf("delete left history: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	if err := filepath.WalkDir(opts.StorageDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (filepath.Ext(path) == ".db" || filepath.Ext(path) == ".db-wal" || filepath.Ext(path) == ".db-shm") {
			t.Errorf("runtime created SQLite: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthRuntime(t *testing.T) {
	db, _ := test_utils.PostgresStore(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	opts := &WikiOptions{Postgres: db, StorageDir: dir, AdminUsername: "admin", AdminEmail: "admin@example.com", AdminPassword: "admin-password", JWTSecret: "postgres-auth-runtime-test-secret-32-bytes", AccessTokenTimeout: time.Hour, RefreshTokenTimeout: 24 * time.Hour, EnableAPIKeyManagement: true, SMTP: email.Config{Host: "unused.invalid"}}
	observeNoSQLite(t, opts.StorageDir)
	w, err := NewWiki(opts)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			if err := w.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	login, err := w.auth.Login("admin", "admin-password")
	if err != nil {
		t.Fatal(err)
	}
	user, err := w.user.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	key, raw, err := w.apiKeys.CreateAPIKey(auth.CreateAPIKeyParams{Name: "persist", UserID: user.ID, CreatedBy: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.favorites.Add(user.ID, "existing-missing-page"); err != nil {
		t.Fatal(err)
	}
	autoSave := true
	if _, err := w.userSettings.Update(user.ID, usersettings.UserSettingsPatch{AutoSave: &autoSave}); err != nil {
		t.Fatal(err)
	}
	if err := w.branding.UpdateBranding("Persisted Wiki"); err != nil {
		t.Fatal(err)
	}
	token, err := w.emailTokenStore.Issue(user.ID, auth.PurposeInvite, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	// A conflicting legacy file must neither be opened nor used to reset users.
	if err := os.WriteFile(filepath.Join(dir, "users.db"), []byte("untouched legacy bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err = NewWiki(opts)
	if err != nil {
		t.Fatal(err)
	}
	closed = false
	refreshed, err := w.auth.RefreshToken(login.RefreshToken)
	if err != nil {
		t.Fatal("refresh session did not survive restart")
	}
	if err := w.auth.RevokeRefreshToken(refreshed.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := w.auth.RefreshToken(refreshed.RefreshToken); err == nil {
		t.Fatal("logout left refresh token active")
	}
	settings, err := w.userSettings.Get(user.ID)
	if err != nil || !settings.AutoSave {
		t.Fatalf("user settings persistence: %+v %v", settings, err)
	}
	if _, err := w.apiKeys.Resolve(raw); err != nil {
		t.Fatal("API key did not survive restart")
	}
	if _, err := w.emailTokenStore.Resolve(token, auth.PurposeInvite); err != nil {
		t.Fatal("email token did not survive restart")
	}
	if err := w.apiKeys.RevokeAPIKey(key.ID); err != nil {
		t.Fatal(err)
	}
	favs, err := w.favorites.ListPageIDsForUser(user.ID)
	if err != nil || len(favs) != 1 {
		t.Fatal("favorites did not survive restart")
	}
	branding, err := w.branding.GetBranding()
	if err != nil || branding.SiteName != "Persisted Wiki" {
		t.Fatal("branding did not survive restart")
	}
	for _, file := range []string{"sessions.db", "api_keys.db", "email_tokens.db", "favorites.db", "usersettings.db", "branding.json", "links.db", "tags.db", "properties.db", "search.db"} {
		if _, err := os.Stat(filepath.Join(dir, file)); !os.IsNotExist(err) {
			t.Fatalf("runtime opened legacy store %s", file)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "users.db"))
	if err != nil || string(data) != "untouched legacy bytes" {
		t.Fatal("runtime changed legacy user file")
	}
}
