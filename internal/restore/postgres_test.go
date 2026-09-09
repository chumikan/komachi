package restore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/snapshot"
	"github.com/perber/wiki/internal/test_utils"
	"github.com/perber/wiki/internal/transfer"
	"golang.org/x/crypto/bcrypt"
)

func TestPostgresLiveSnapshotAndUploadedRestore(t *testing.T) {
	ctx := context.Background()
	pg, cfg := test_utils.PostgresStore(t)
	tools := transfer.PGTools{Dump: os.Getenv("LEAFWIKI_TEST_PG_DUMP"), Restore: os.Getenv("LEAFWIKI_TEST_PG_RESTORE")}
	for _, v := range []struct{ path, name string }{{tools.Dump, "pg_dump"}, {tools.Restore, "pg_restore"}} {
		path := v.path
		if path == "" {
			path = v.name
		}
		if _, err := exec.LookPath(path); err != nil {
			t.Skip("requires PostgreSQL clients")
		}
	}
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset := filepath.Join(dir, "assets", "page", "file.txt")
	if err := os.MkdirAll(filepath.Dir(asset), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte("original asset"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.DB().Exec(ctx, `INSERT INTO pages(id,parent_id,title,slug,kind,position,metadata,content_markdown) VALUES('page','root','Title','page','page',0,'{"creatorId":"system","lastAuthorId":"system","createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-01T00:00:00Z"}','original body')`); err != nil {
		t.Fatal(err)
	}
	us := auth.NewPostgresUserStore(pg)
	defer us.Close()
	ss := auth.NewPostgresSessionStore(pg)
	defer ss.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := us.CreateUser(&auth.User{ID: "user", Username: "admin", Email: "admin@example.org", Password: string(hash), Role: auth.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	service := auth.NewAuthService(auth.NewUserService(us), auth.NewSessionManager(ss, strings.Repeat("s", 32), time.Hour, 24*time.Hour), nil)
	if _, err := service.Login("admin", "password"); err != nil {
		t.Fatal(err)
	}
	gate := NewWriteGate()
	snapshots := snapshot.NewManager(snapshot.Config{Postgres: true, Database: pg, DatabaseURL: cfg.URL, DataDir: dir, BackupsDir: filepath.Join(dir, "snapshots"), Freeze: gate.Freeze, PGTools: tools, WikiVersion: "test"})
	leave, ok := gate.TryEnter()
	if !ok {
		t.Fatal("unexpected gate")
	}
	done := make(chan error, 1)
	go func() { done <- snapshots.RunOnce(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for !gate.Engaged() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !gate.Engaged() {
		leave()
		t.Fatal("snapshot did not engage gate")
	}
	select {
	case <-done:
		leave()
		t.Fatal("snapshot did not drain in-flight write")
	default:
	}
	leave()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "snapshots", "*.zip"))
	if err != nil || len(paths) != 1 {
		t.Fatal("complete snapshot missing")
	}
	if _, err := pg.DB().Exec(ctx, `UPDATE pages SET content_markdown='changed after backup' WHERE id='page'`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte("changed asset"), 0600); err != nil {
		t.Fatal(err)
	}
	var resync atomic.Int32
	manager := NewManager(Config{Database: pg, DatabaseURL: cfg.URL, PGTools: tools, DataDir: dir, WriteGate: gate, SnapshotManager: snapshots, AuthService: service, TriggerResync: func() { resync.Add(1) }})
	in, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	err = manager.TriggerRestoreFromUpload(in)
	in.Close()
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	status := manager.Status()
	if status.Error != "" || !status.Done || status.NeedsIntervention {
		t.Fatalf("live restore: %+v", status)
	}
	if gate.Engaged() || resync.Load() != 1 {
		t.Fatal("restore did not resume writes/resync")
	}
	var body string
	if err := pg.DB().QueryRow(ctx, `SELECT content_markdown FROM pages WHERE id='page'`).Scan(&body); err != nil || body != "original body" {
		t.Fatal("page did not restore")
	}
	raw, err := os.ReadFile(asset)
	if err != nil || string(raw) != "original asset" {
		t.Fatal("asset did not restore")
	}
	var active int
	if err := pg.DB().QueryRow(ctx, `SELECT count(*) FROM sessions WHERE revoked_at IS NULL`).Scan(&active); err != nil || active != 0 {
		t.Fatal("live restore did not invalidate sessions")
	}
	if _, err := service.Login("admin", "password"); err != nil {
		t.Fatal("existing auth repository failed after restore")
	}
}

func TestPostgresFreezeCancellationDoesNotProceed(t *testing.T) {
	gate := NewWriteGate()
	leave, _ := gate.TryEnter()
	defer leave()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.Freeze(ctx); err == nil {
		t.Fatal("ignored cancellation while waiting for writes")
	}
	if gate.Engaged() {
		t.Fatal("cancelled preflight left gate engaged")
	}
}
