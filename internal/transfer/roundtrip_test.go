package transfer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/core/revision"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/favorites"
	"github.com/perber/wiki/internal/storage/postgres"
	"github.com/perber/wiki/internal/test_utils"
	"github.com/perber/wiki/internal/usersettings"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

func integrationPGTools(t *testing.T) PGTools {
	t.Helper()
	p := PGTools{Dump: os.Getenv("LEAFWIKI_TEST_PG_DUMP"), Restore: os.Getenv("LEAFWIKI_TEST_PG_RESTORE")}
	for _, command := range []struct{ override, name string }{{p.Dump, "pg_dump"}, {p.Restore, "pg_restore"}} {
		path := command.override
		if path == "" {
			path = command.name
		}
		if _, err := exec.LookPath(path); err != nil {
			t.Skip("PostgreSQL client unavailable: " + command.name)
		}
	}
	return p
}

func compositeFixture(t *testing.T) (string, string, string, string, string) {
	t.Helper()
	source, rev := revisionFixture(t)
	writeFixture(t, source, "root/group/index.md", pageFixture("group-id", "Section"))
	writeFixture(t, source, "root/group/child.md", strings.ReplaceAll(pageFixture("child-id", "別ページ"), "[[別ページ]]", "[[ページ]]"))
	writeFixture(t, source, "root/.order.json", `{"ordered_ids":["group-id","page-id"]}`)
	writeFixture(t, source, "assets/page-id/current.txt", "current attachment")
	writeFixture(t, source, "avatars/original-user.png", "avatar fixture")
	writeFixture(t, source, "branding/logo.png", "branding fixture")
	writeFixture(t, source, "branding.json", `{"siteName":"Imported Wiki","logoFile":"logo.png"}`)
	writeFixture(t, source, "public-access.json", `{"enabled":true}`)
	fs := revision.NewFSStore(source, nil)
	historical := filepath.Join(t.TempDir(), "historical.txt")
	if err := os.WriteFile(historical, []byte("historical attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	assetHash, size, err := fs.SaveAssetBlobFromPath(historical)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash, err := fs.SaveAssetManifest([]revision.AssetRef{{Name: "historical.txt", SHA256: assetHash, SizeBytes: size}})
	if err != nil {
		t.Fatal(err)
	}
	rev.AssetManifestHash = manifestHash
	if err := fs.SaveRevision(rev); err != nil {
		t.Fatal(err)
	}
	// Import must not coalesce existing same-author history even one second apart.
	next := *rev
	next.ID = "second-revision-id"
	next.CreatedAt = rev.CreatedAt.Add(time.Second)
	if err := fs.SaveRevision(&next); err != nil {
		t.Fatal(err)
	}
	us, err := auth.NewUserStore(source)
	if err != nil {
		t.Fatal(err)
	}
	defer us.Close()
	ss, err := auth.NewSessionStore(source)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	ks, err := auth.NewAPIKeyStore(source)
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()
	es, err := auth.NewEmailTokenStore(source)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("existing-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct{ id, name string }{{"original-user", "original"}, {"totp-user", "twofactor"}} {
		if err := us.CreateUser(&auth.User{ID: user.id, Username: user.name, Email: user.name + "@example.org", Role: auth.RoleAdmin, Password: string(hash)}); err != nil {
			t.Fatal(err)
		}
	}
	totpService, err := auth.NewTOTPService([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	generated, err := totpService.GenerateSecret("twofactor@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := us.EnableTOTP("totp-user", generated.EncryptedSecret, []string{string(hash)}); err != nil {
		t.Fatal(err)
	}
	manager := auth.NewSessionManager(ss, strings.Repeat("j", 32), time.Hour, 24*time.Hour)
	service := auth.NewAuthService(auth.NewUserService(us), manager, totpService)
	login, err := service.Login("original", "existing-password")
	if err != nil {
		t.Fatal(err)
	}
	keyService := auth.NewAPIKeyService(ks, service)
	_, apiKey, err := keyService.CreateAPIKey(auth.CreateAPIKeyParams{Name: "existing key", UserID: "original-user", CreatedBy: "original-user", Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	emailToken, err := es.Issue("original-user", "password_reset", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	favoriteStore, err := favorites.NewFavoritesStore(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := favoriteStore.Add("original-user", "page-id"); err != nil {
		t.Fatal(err)
	}
	favoriteStore.Close()
	settingStore, err := usersettings.NewUserSettingsStore(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := settingStore.Upsert(&usersettings.UserSettings{UserID: "original-user", Language: "de", AutoSave: false, DateFormat: "locale", TimeFormat: "locale", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	settingStore.Close()
	return source, apiKey, emailToken, login.RefreshToken, generated.Secret
}

func tableSnapshots(t *testing.T, pg *postgres.Store) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range append(append([]string{}, canonicalTables...), derivedTables...) {
		var value string
		if err := pg.DB().QueryRow(context.Background(), "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+table+" t").Scan(&value); err != nil {
			t.Fatal(err)
		}
		result[table] = value
	}
	return result
}

func TestPostgresCompleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	sourceDB, sourceConfig := test_utils.PostgresStore(t)
	tools := integrationPGTools(t)
	if err := sourceDB.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	source, key, emailToken, refresh, totpSecret := compositeFixture(t)
	workspace := filepath.Join(t.TempDir(), "imported")
	report, err := ImportLegacy(ctx, sourceDB, source, workspace, false)
	if err != nil || !report.Committed {
		t.Fatalf("import: %+v %v", report, err)
	}
	if report.Counts["revisions"] != 2 {
		t.Fatal("import coalesced existing history")
	}
	before := tableSnapshots(t, sourceDB)
	filesBefore, err := persistentInventory(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "complete.zip")
	if err := CreateBackup(ctx, sourceDB, sourceConfig.URL, workspace, archive, "snapshot-roundtrip", "test", tools); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(archive)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("backup artifact permissions")
	}
	targetDB, targetConfig := test_utils.PostgresStore(t)
	if err := targetDB.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	restored := t.TempDir()
	if err := RestoreBackup(ctx, targetDB, targetConfig.URL, restored, archive, tools, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	after := tableSnapshots(t, targetDB)
	for table, want := range before {
		if after[table] != want {
			t.Fatalf("round-trip changed table %s", table)
		}
	}
	filesAfter, err := persistentInventory(ctx, restored)
	if err != nil || inventoryHash(filesBefore) != inventoryHash(filesAfter) {
		t.Fatal("round-trip changed assets")
	}
	if err := targetDB.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	// Reopen the pool: canonical state must survive process-level reconnection.
	targetDB.ResetConnections()
	us := auth.NewPostgresUserStore(targetDB)
	defer us.Close()
	ss := auth.NewPostgresSessionStore(targetDB)
	defer ss.Close()
	ks := auth.NewPostgresAPIKeyStore(targetDB)
	defer ks.Close()
	es := auth.NewPostgresEmailTokenStore(targetDB)
	defer es.Close()
	totpService, err := auth.NewTOTPService([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewAuthService(auth.NewUserService(us), auth.NewSessionManager(ss, strings.Repeat("j", 32), time.Hour, 24*time.Hour), totpService)
	if _, err := authService.Login("original", "existing-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := authService.RefreshToken(refresh); err != nil {
		t.Fatal("restored refresh session failed")
	}
	if _, err := auth.NewAPIKeyService(ks, authService).Resolve(key); err != nil {
		t.Fatal("restored API key failed")
	}
	if _, err := es.Resolve(emailToken, "password_reset"); err != nil {
		t.Fatal("restored email token failed")
	}
	user, err := us.GetUserByID("totp-user")
	if err != nil || !user.TOTPEnabled || user.TOTPSecretEncrypted == "" {
		t.Fatal("TOTP data lost")
	}
	challenge, err := authService.Login("twofactor", "existing-password")
	if err != nil || !challenge.RequiresTOTP {
		t.Fatal("restored TOTP challenge failed")
	}
	code, err := totp.GenerateCode(totpSecret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authService.CompleteTOTPLogin(challenge.LoginChallengeToken, code); err != nil {
		t.Fatal("restored TOTP login failed")
	}
	pageTree := tree.NewPostgresTreeService(restored, targetDB)
	settingsStore := usersettings.NewPostgresUserSettingsStore(targetDB, nil)
	defer settingsStore.Close()
	settings, err := settingsStore.Get("original-user")
	if err != nil || settings.Language != "de" || settings.AutoSave {
		t.Fatalf("restored user settings cannot be read: %v", err)
	}
	if err := pageTree.LoadTree(); err != nil {
		t.Fatal(err)
	}
	revisions := revision.NewService(restored, pageTree, nil)
	if err := revisions.RestoreRevision("page-id", "original-revision-id", "system"); err != nil {
		t.Fatal(err)
	}
	page, err := pageTree.GetPage("page-id")
	if err != nil || page.Content != "# 過去の本文\n" || page.Slug != "page" {
		t.Fatal("revision restore semantics changed")
	}
	content, err := os.ReadFile(filepath.Join(restored, "assets", "page-id", "historical.txt"))
	if err != nil || string(content) != "historical attachment" {
		t.Fatal("historical attachment restore failed")
	}
	if err := RestoreBackup(ctx, targetDB, targetConfig.URL, restored, archive, tools, RestoreOptions{}); err == nil {
		t.Fatal("destructive restore lacked explicit replacement")
	}
}

func TestPostgresEmptyWorkspaceRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, cfg := test_utils.PostgresStore(t)
	tools := integrationPGTools(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	target := t.TempDir()
	if report, err := ImportLegacy(ctx, db, source, target, true); err != nil || report.Committed {
		t.Fatalf("empty dry-run: %v", err)
	}
	if report, err := ImportLegacy(ctx, db, source, target, false); err != nil || !report.Committed {
		t.Fatalf("empty import: %v", err)
	}
	if _, err := ImportLegacy(ctx, db, source, target, false); err == nil {
		t.Fatal("empty duplicate import accepted")
	}
	before := tableSnapshots(t, db)
	archive := filepath.Join(t.TempDir(), "empty.zip")
	if err := CreateBackup(ctx, db, cfg.URL, target, archive, "empty", "test", tools); err != nil {
		t.Fatal(err)
	}
	restored, restoredCfg := test_utils.PostgresStore(t)
	if err := restored.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackup(ctx, restored, restoredCfg.URL, t.TempDir(), archive, tools, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	after := tableSnapshots(t, restored)
	for table, want := range before {
		if after[table] != want {
			t.Fatalf("empty round-trip changed %s", table)
		}
	}
}
