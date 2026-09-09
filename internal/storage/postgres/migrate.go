package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLockID int64 = 0x4c45414657494b49 // LEAFWIKI, transaction-scoped

var migrationName = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

type MigrationStatus struct {
	Version   int
	Name      string
	AppliedAt *time.Time
}

type Status struct {
	PostgreSQLVersion string
	PGroongaVersion   string
	Migrations        []MigrationStatus
}

func loadMigrations(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var migrations []migration
	for _, entry := range entries {
		match := migrationName.FindStringSubmatch(entry.Name())
		if entry.IsDir() || match == nil {
			return nil, fmt.Errorf("invalid migration filename: %s", entry.Name())
		}
		version, _ := strconv.Atoi(match[1])
		data, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, fmt.Errorf("empty migration: %s", entry.Name())
		}
		migrations = append(migrations, migration{
			version: version, name: entry.Name(), sql: string(data),
			checksum: fmt.Sprintf("%x", sha256.Sum256(data)),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	if len(migrations) == 0 {
		return nil, errors.New("no PostgreSQL migrations embedded")
	}
	for i, m := range migrations {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1: %s", m.name)
		}
	}
	return migrations, nil
}

func embeddedMigrations() ([]migration, error) {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	return loadMigrations(files)
}

// Migrate applies all pending migrations and their history in one transaction.
// A transaction-scoped lock serializes concurrent migrators and is released even
// on connection loss. A failure rolls back the entire pending batch. Migration
// SQL must be transaction-safe; no CONCURRENTLY, transaction control, or VACUUM.
func (s *Store) Migrate(ctx context.Context) error {
	migrations, err := embeddedMigrations()
	if err != nil {
		return err
	}
	return s.migrate(ctx, migrations)
}

func (s *Store) migrate(ctx context.Context, migrations []migration) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
			return fmt.Errorf("lock PostgreSQL migrations: %w", err)
		}
		if err := checkServer(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
			version INTEGER PRIMARY KEY CHECK (version > 0),
			name TEXT NOT NULL UNIQUE,
			checksum TEXT NOT NULL CHECK (length(checksum) = 64),
			applied_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
		)`); err != nil {
			return fmt.Errorf("create migration history: %w", err)
		}
		applied, err := readApplied(ctx, tx, migrations)
		if err != nil {
			return err
		}
		for _, m := range migrations[len(applied):] {
			// Scripts are embedded, reviewed SQL, not user input. No arguments
			// means pgx uses the simple protocol, allowing several statements.
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return fmt.Errorf("apply migration %s: %w", m.name, err)
			}
			if _, err := tx.Exec(ctx,
				"INSERT INTO public.schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
				m.version, m.name, m.checksum); err != nil {
				return fmt.Errorf("record migration %s: %w", m.name, err)
			}
		}
		_, err = extensionVersion(ctx, tx, true)
		return err
	})
}

// readApplied rejects changed scripts, unknown versions, and holes in history.
// A newer binary may have pending migrations; an older binary must fail closed.
func readApplied(ctx context.Context, db DBTX, migrations []migration) ([]time.Time, error) {
	var exists bool
	if err := db.QueryRow(ctx, "SELECT to_regclass('public.schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	rows, err := db.Query(ctx, "SELECT version, name, checksum, applied_at FROM public.schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var applied []time.Time
	for rows.Next() {
		var version int
		var name, checksum string
		var at time.Time
		if err := rows.Scan(&version, &name, &checksum, &at); err != nil {
			return nil, err
		}
		if version != len(applied)+1 || version > len(migrations) {
			return nil, fmt.Errorf("unknown or non-contiguous PostgreSQL migration version %d", version)
		}
		m := migrations[version-1]
		if name != m.name || checksum != m.checksum {
			return nil, fmt.Errorf("PostgreSQL migration %d differs from the embedded migration", version)
		}
		applied = append(applied, at)
	}
	return applied, rows.Err()
}

func checkServer(ctx context.Context, db DBTX) error {
	var version int
	var encoding string
	if err := db.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer, current_setting('server_encoding')").Scan(&version, &encoding); err != nil {
		return err
	}
	if version < 170000 || encoding != "UTF8" {
		return errors.New("LeafWiki PostgreSQL storage requires PostgreSQL 17 or newer and UTF8 encoding")
	}
	return nil
}

func extensionVersion(ctx context.Context, db DBTX, required bool) (string, error) {
	var version, schema string
	err := db.QueryRow(ctx, `SELECT e.extversion, n.nspname
		FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pgroonga'`).Scan(&version, &schema)
	if errors.Is(err, pgx.ErrNoRows) {
		if required {
			return "", errors.New("PGroonga extension is missing; run leafwiki database migrate")
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if schema != "public" {
		return "", errors.New("PGroonga extension must be installed in public")
	}
	return version, nil
}

// Status only reads the database; even an empty database stays empty.
func (s *Store) Status(ctx context.Context) (*Status, error) {
	migrations, err := embeddedMigrations()
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	if err := checkServer(ctx, tx); err != nil {
		return nil, err
	}
	applied, err := readApplied(ctx, tx, migrations)
	if err != nil {
		return nil, err
	}
	status := &Status{}
	if err := tx.QueryRow(ctx, "SHOW server_version").Scan(&status.PostgreSQLVersion); err != nil {
		return nil, err
	}
	status.PGroongaVersion, err = extensionVersion(ctx, tx, len(applied) > 0)
	if err != nil {
		return nil, err
	}
	for i, m := range migrations {
		entry := MigrationStatus{Version: m.version, Name: m.name}
		if i < len(applied) {
			entry.AppliedAt = &applied[i]
		}
		status.Migrations = append(status.Migrations, entry)
	}
	return status, tx.Commit(ctx)
}

// CheckSchema is the read-only readiness check for future PostgreSQL wiring.
// It never applies pending migrations as a side effect of starting a server.
func (s *Store) CheckSchema(ctx context.Context) error {
	status, err := s.Status(ctx)
	if err != nil {
		return err
	}
	for _, m := range status.Migrations {
		if m.AppliedAt == nil {
			return errors.New("PostgreSQL migrations are pending; run leafwiki database migrate")
		}
	}
	return nil
}
