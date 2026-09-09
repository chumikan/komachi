package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

// Every test gets a new database. Never drop or migrate the database from the
// URL itself. The integration role needs CREATEDB and permission for PGroonga.
func integrationStore(t *testing.T) (*Store, Config) {
	t.Helper()
	url := os.Getenv("LEAFWIKI_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LEAFWIKI_TEST_DATABASE_URL to run real PostgreSQL/PGroonga integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "leafwiki_test_" + hex.EncodeToString(suffix[:])
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("clean up test database: %v", err)
		}
		_ = admin.Close(ctx)
	})
	config := DefaultConfig(url)
	// Preserve credentials/TLS parameters while selecting the isolated database.
	if strings.HasPrefix(url, "postgres://") || strings.HasPrefix(url, "postgresql://") {
		parsed, err := neturl.Parse(url)
		if err != nil {
			t.Fatal("invalid integration URL")
		}
		parsed.Path = "/" + name
		params := parsed.Query()
		params.Del("dbname")
		params.Del("database")
		parsed.RawQuery = params.Encode()
		config.URL = parsed.String()
	} else {
		config.URL = url + " dbname=" + name
	}
	store, err := Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	var actual string
	if err := store.DB().QueryRow(ctx, "SELECT current_database()").Scan(&actual); err != nil || actual != name {
		t.Fatalf("database isolation failed: %q, %v", actual, err)
	}
	return store, config
}

func mustExec(t *testing.T, db DBTX, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationMigrations(t *testing.T) {
	s, config := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	status, err := s.Status(ctx)
	if err != nil || status.Migrations[0].AppliedAt != nil || status.PGroongaVersion != "" {
		t.Fatalf("empty status: %+v, %v", status, err)
	}
	if err := s.CheckSchema(ctx); err == nil {
		t.Fatal("empty database reported ready")
	}
	var exists bool
	if err := s.DB().QueryRow(ctx, "SELECT to_regclass('public.schema_migrations') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("Open/Status/CheckSchema must not create schema: %v", err)
	}
	second, err := Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	errs := make(chan error, 2)
	for _, store := range []*Store{s, second} {
		go func() { errs <- store.Migrate(ctx) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	status, err = s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.PGroongaVersion != "4.0.8" || status.Migrations[0].AppliedAt == nil {
		t.Fatalf("unexpected pinned environment: %+v", status)
	}
	t.Logf("PostgreSQL %s, PGroonga %s", status.PostgreSQLVersion, status.PGroongaVersion)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := second.Status(ctx)
	if err != nil || !after.Migrations[0].AppliedAt.Equal(*status.Migrations[0].AppliedAt) {
		t.Fatalf("repeat migration rewrote history: %v", err)
	}
	if err := s.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	// Phase 3 adds only pages, revisions, content and attachment manifests.
	var tables int
	if err := s.DB().QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'").Scan(&tables); err != nil || tables != 17 {
		t.Fatalf("unexpected application tables: %d, %v", tables, err)
	}
}

func TestIntegrationPhase2Upgrade(t *testing.T) {
	s, _ := integrationStore(t)
	ctx := context.Background()
	m, e := embeddedMigrations()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.migrate(ctx, m[:1]); e != nil {
		t.Fatal(e)
	}
	var checksum string
	if e = s.DB().QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=1").Scan(&checksum); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckSchema(ctx); e != nil {
		t.Fatal(e)
	}
	var after string
	if e = s.DB().QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=1").Scan(&after); e != nil || after != checksum {
		t.Fatalf("migration 1 changed: %v", e)
	}
}

func TestIntegrationPhase5Upgrade(t *testing.T) {
	s, _ := integrationStore(t)
	ctx := context.Background()
	m, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx, m[:4]); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB(), `INSERT INTO favorites(user_id,page_id,created_at) VALUES('existing-user','existing-page','2025-01-01T00:00:00.123456Z')`)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var original *string
	var nanos int
	if err := s.DB().QueryRow(ctx, `SELECT created_at_legacy,created_at_submicro FROM favorites WHERE user_id='existing-user'`).Scan(&original, &nanos); err != nil || original != nil || nanos != 0 {
		t.Fatal("Phase 5 timestamp changed")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationMigrationRollbackAndDrift(t *testing.T) {
	s, _ := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	base, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		base[4].name:     {Data: []byte(base[4].sql)},
		base[3].name:     {Data: []byte(base[3].sql)},
		base[2].name:     {Data: []byte(base[2].sql)},
		base[1].name:     {Data: []byte(base[1].sql)},
		base[0].name:     {Data: []byte(base[0].sql)},
		"0006_probe.sql": {Data: []byte("CREATE TABLE public.migration_probe(id integer);")},
		"0007_fail.sql":  {Data: []byte("SELECT definitely_missing_function();")},
	}
	pending, err := loadMigrations(files)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx, pending); err == nil {
		t.Fatal("expected failed migration")
	}
	var clean bool
	if err := s.DB().QueryRow(ctx, `SELECT to_regclass('public.migration_probe') IS NULL
		AND to_regclass('public.schema_migrations') IS NULL
		AND NOT EXISTS (SELECT FROM pg_extension WHERE extname = 'pgroonga')`).Scan(&clean); err != nil || !clean {
		t.Fatalf("failed batch left schema/history/extension behind: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx, pending); err == nil {
		t.Fatal("expected pending batch rollback")
	}
	if err := s.CheckSchema(ctx); err != nil {
		t.Fatalf("failed pending batch damaged prior migration: %v", err)
	}
	if err := s.DB().QueryRow(ctx, "SELECT to_regclass('public.migration_probe') IS NULL").Scan(&clean); err != nil || !clean {
		t.Fatalf("failed pending batch retained a table: %v", err)
	}
	mustExec(t, s.DB(), "UPDATE public.schema_migrations SET checksum = repeat('0', 64) WHERE version = 1")
	if err := s.Migrate(ctx); err == nil {
		t.Fatal("accepted changed migration")
	}
	if _, err := s.Status(ctx); err == nil {
		t.Fatal("status accepted changed migration")
	}
	mustExec(t, s.DB(), "UPDATE public.schema_migrations SET checksum = $1 WHERE version = 1", base[0].checksum)
	mustExec(t, s.DB(), "INSERT INTO public.schema_migrations(version, name, checksum) VALUES (6, 'unknown', repeat('0', 64))")
	if err := s.Migrate(ctx); err == nil {
		t.Fatal("accepted unknown/holey migration history")
	}
}

func TestIntegrationMigrationLockCancellation(t *testing.T) {
	s, _ := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lock, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(ctx) }()
	mustExec(t, lock, "SELECT pg_advisory_xact_lock($1)", migrationLockID)
	waitCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err = s.Migrate(waitCtx)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("migration lock did not honor cancellation: %v", err)
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migration did not recover after lock cancellation: %v", err)
	}
}

func TestIntegrationTransactions(t *testing.T) {
	s, config := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	mustExec(t, s.DB(), "CREATE TABLE tx_probe(id integer PRIMARY KEY, content text NOT NULL, version integer NOT NULL DEFAULT 0)")
	if err := s.WithTx(ctx, func(db DBTX) error {
		_, err := db.Exec(ctx, "INSERT INTO tx_probe(id, content) VALUES ($1, $2)", 1, "日本語'; DROP TABLE tx_probe; --")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("abort")
	if err := s.WithTx(ctx, func(db DBTX) error {
		if _, err := db.Exec(ctx, "INSERT INTO tx_probe(id, content) VALUES (2, 'rollback')"); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("lost callback error: %v", err)
	}
	if err := s.WithTx(ctx, func(db DBTX) error {
		if _, err := db.Exec(ctx, "INSERT INTO tx_probe(id, content) VALUES (3, 'rollback')"); err != nil {
			return err
		}
		_, err := db.Exec(ctx, "INSERT INTO tx_probe(id, content) VALUES (1, 'duplicate')")
		return err
	}); err == nil {
		t.Fatal("constraint violation committed")
	}
	func() {
		defer func() {
			if recover() != sentinel {
				t.Error("transaction did not preserve panic")
			}
		}()
		_ = s.WithTx(ctx, func(db DBTX) error {
			mustExec(t, db, "INSERT INTO tx_probe(id, content) VALUES (4, 'panic')")
			panic(sentinel)
		})
	}()
	cancelled, abort := context.WithCancel(ctx)
	if err := s.WithTx(cancelled, func(db DBTX) error {
		mustExec(t, db, "INSERT INTO tx_probe(id, content) VALUES (5, 'cancel')")
		abort()
		return nil
	}); err == nil {
		t.Fatal("cancelled transaction committed")
	}
	abort()
	// Closing/reopening the application pool must retain committed data only.
	s.Close()
	reopened, err := Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var count int
	var content string
	if err := reopened.DB().QueryRow(ctx, "SELECT count(*), min(content) FROM tx_probe").Scan(&count, &content); err != nil || count != 1 || content != "日本語'; DROP TABLE tx_probe; --" {
		t.Fatalf("transaction durability/rollback/parameter binding: %d, %q, %v", count, content, err)
	}
	// Two independent transactions attempt the same optimistic update. The
	// storage primitive must permit exactly one success, not lose an update.
	var wg sync.WaitGroup
	results := make(chan int64, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			err := reopened.WithTx(ctx, func(db DBTX) error {
				tag, err := db.Exec(ctx, "UPDATE tx_probe SET version = version + 1 WHERE id = $1 AND version = $2", 1, 0)
				if err == nil {
					results <- tag.RowsAffected()
				}
				return err
			})
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var updated int64
	for n := range results {
		updated += n
	}
	if updated != 1 {
		t.Fatalf("concurrent update affected %d rows", updated)
	}
}

func TestIntegrationConnectionLossAndPoolLimits(t *testing.T) {
	_, config := integrationStore(t)
	config.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	killer, err := pgx.Connect(ctx, config.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = killer.Close(ctx) }()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	if err := s.Ping(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("pool did not honor limit/deadline: %v", err)
	}
	stop()
	conn.Release()
	mustExec(t, s.DB(), "CREATE TABLE disconnect_probe(id integer PRIMARY KEY)")
	err = s.WithTx(ctx, func(db DBTX) error {
		if _, err := db.Exec(ctx, "INSERT INTO disconnect_probe VALUES (1)"); err != nil {
			return err
		}
		var pid int32
		if err := db.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			return err
		}
		var killed bool
		if err := killer.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&killed); err != nil || !killed {
			return fmt.Errorf("terminate test connection: %v", err)
		}
		_, err := db.Exec(ctx, "SELECT 1")
		return err
	})
	if err == nil {
		t.Fatal("lost connection was not reported")
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("pool did not replace lost connection: %v", err)
	}
	var count int
	if err := s.DB().QueryRow(ctx, "SELECT count(*) FROM disconnect_probe").Scan(&count); err != nil || count != 0 {
		t.Fatalf("lost connection did not roll back: %d, %v", count, err)
	}
}

func TestIntegrationPGroonga(t *testing.T) {
	s, _ := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Probe tables only: the application search repository is Phase 5 work.
	mustExec(t, s.DB(), "CREATE TABLE search_probe(id integer PRIMARY KEY, title text, body text)")
	mustExec(t, s.DB(), "CREATE INDEX search_probe_index ON search_probe USING pgroonga ((ARRAY[title, body]))")
	mustExec(t, s.DB(), "INSERT INTO search_probe VALUES (2, $1, $2), (1, $3, $4)", "東京の図書館", "日本語の検索を確認します", "別のページ", "今日は東京へ行きます")
	query := `SELECT id FROM search_probe
		WHERE ARRAY[title, body] &@ ($1, ARRAY[5, 1], 'search_probe_index')::pgroonga_full_text_search_condition
		ORDER BY pgroonga_score(tableoid, ctid) DESC, id`
	check := func() {
		t.Helper()
		if err := s.WithTx(ctx, func(db DBTX) error {
			// Small fixtures otherwise use a sequential scan and score zero.
			if _, err := db.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
				return err
			}
			rows, err := db.Query(ctx, query, "東京")
			if err != nil {
				return err
			}
			defer rows.Close()
			var ids []int
			for rows.Next() {
				var id int
				if err := rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
			if fmt.Sprint(ids) != "[2 1]" {
				return fmt.Errorf("Japanese search/title weighting: %v", ids)
			}
			return rows.Err()
		}); err != nil {
			t.Fatal(err)
		}
	}
	check()
	mustExec(t, s.DB(), "DROP INDEX search_probe_index")
	mustExec(t, s.DB(), "CREATE INDEX search_probe_index ON search_probe USING pgroonga ((ARRAY[title, body]))")
	check()
}

func TestIntegrationPhase3Upgrade(t *testing.T) {
	s, _ := integrationStore(t)
	m, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.migrate(ctx, m[:2]); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, old := range m[:2] {
		var checksum string
		if err := s.DB().QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=$1", old.version).Scan(&checksum); err != nil || checksum != old.checksum {
			t.Fatalf("previous migration changed: %v", err)
		}
	}
}

func TestIntegrationPhase4Upgrade(t *testing.T) {
	s, _ := integrationStore(t)
	m, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.migrate(ctx, m[:3]); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, old := range m[:3] {
		var checksum string
		if err := s.DB().QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=$1", old.version).Scan(&checksum); err != nil || checksum != old.checksum {
			t.Fatalf("previous migration changed: %v", err)
		}
	}
}
