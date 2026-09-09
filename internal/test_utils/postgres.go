package test_utils

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/jackc/pgx/v5"
	"github.com/perber/wiki/internal/storage/postgres"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// PostgresStore creates an isolated, unmigrated integration-test database.
// It never migrates or drops the database named in the supplied environment URL.
func PostgresStore(t *testing.T) (*postgres.Store, postgres.Config) {
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
	config := postgres.DefaultConfig(url)
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
	store, err := postgres.Open(ctx, config)
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
