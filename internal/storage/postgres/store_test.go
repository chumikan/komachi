package postgres

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestConfig(t *testing.T) {
	cfg := DefaultConfig("postgres://user:private-password@localhost/wiki?sslmode=verify-full")
	pool, err := cfg.poolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if pool.MaxConns != 8 || pool.MinConns != 0 || pool.ConnConfig.TLSConfig == nil {
		t.Fatal("pool limits or requested TLS configuration lost")
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.URL = "" },
		func(c *Config) { c.MaxConns = 0 },
		func(c *Config) { c.MinConns = -1 },
		func(c *Config) { c.MinConns = c.MaxConns + 1 },
		func(c *Config) { c.ConnectTimeout = 0 },
		func(c *Config) { c.MaxConnLifetime = -1 },
		func(c *Config) { c.MaxConnIdleTime = 0 },
		func(c *Config) { c.URL = "postgres://user:private-password@host:invalid/wiki" },
	} {
		invalid := cfg
		mutate(&invalid)
		if _, err := invalid.poolConfig(); err == nil || strings.Contains(err.Error(), "private-password") {
			t.Fatalf("expected validation error without credentials, got %v", err)
		}
	}
}

func TestMigrationFiles(t *testing.T) {
	for _, names := range [][]string{
		{}, {"0000_zero.sql"}, {"0002_gap.sql"}, {"0001_a.sql", "0001_b.sql"}, {"001_short.sql"}, {"README.md"},
	} {
		files := fstest.MapFS{}
		for _, name := range names {
			files[name] = &fstest.MapFile{Data: []byte("SELECT 1;")}
		}
		if _, err := loadMigrations(files); err == nil {
			t.Fatalf("accepted invalid migration names: %v", names)
		}
	}
	files := fstest.MapFS{"0001_empty.sql": {Data: []byte(" \n")}}
	if _, err := loadMigrations(files); err == nil {
		t.Fatal("accepted empty migration")
	}
	migrations, err := embeddedMigrations()
	if err != nil || len(migrations) != 5 || migrations[0].version != 1 {
		t.Fatalf("embedded migrations: %v, %v", migrations, err)
	}
}
