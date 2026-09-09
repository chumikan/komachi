// Package postgres owns PostgreSQL connections, transactions, and migrations.
// Domain repositories belong in the storage layer and can share a DBTX, whether
// backed by a pool or an existing transaction. HTTP handlers must not use DBTX.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is the query surface shared by pgxpool.Pool and pgx.Tx. Repositories
// accept this interface so one use case can commit several repositories together.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	ConnectTimeout  time.Duration
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

func DefaultConfig(databaseURL string) Config {
	return Config{
		URL: databaseURL, MaxConns: 8,
		ConnectTimeout:  5 * time.Second,
		MaxConnLifetime: 30 * time.Minute, MaxConnIdleTime: 5 * time.Minute,
	}
}

func (c Config) poolConfig() (*pgxpool.Config, error) {
	if strings.TrimSpace(c.URL) == "" {
		return nil, errors.New("PostgreSQL database URL is required")
	}
	if c.MaxConns <= 0 || c.MinConns < 0 || c.MinConns > c.MaxConns {
		return nil, errors.New("PostgreSQL pool requires 0 <= min connections <= max connections and max > 0")
	}
	if c.ConnectTimeout <= 0 || c.MaxConnLifetime <= 0 || c.MaxConnIdleTime <= 0 {
		return nil, errors.New("PostgreSQL connection timeouts and lifetimes must be positive")
	}
	cfg, err := pgxpool.ParseConfig(c.URL)
	if err != nil {
		// pgx parse errors can contain the original DSN, including credentials.
		return nil, errors.New("invalid PostgreSQL database URL or connection parameters")
	}
	cfg.MaxConns, cfg.MinConns = c.MaxConns, c.MinConns
	cfg.MaxConnLifetime, cfg.MaxConnIdleTime = c.MaxConnLifetime, c.MaxConnIdleTime
	cfg.ConnConfig.ConnectTimeout = c.ConnectTimeout
	cfg.ConnConfig.RuntimeParams["application_name"] = "leafwiki"
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	// This phase supports a dedicated database using public, not configurable
	// tenant schemas. Keep extension functions and repository SQL predictable.
	cfg.ConnConfig.RuntimeParams["search_path"] = "public"
	return cfg, nil
}

type Store struct {
	pool *pgxpool.Pool
}

// Open verifies connectivity. It never creates or modifies a schema.
func Open(ctx context.Context, config Config) (*Store, error) {
	cfg, err := config.poolConfig()
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, config.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// ResetConnections discards cached connections after an explicit schema/data
// restore. Existing repository adapters keep using this same pool.
func (s *Store) ResetConnections() { s.pool.Reset() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// DB is for storage repositories, not services or handlers.
func (s *Store) DB() DBTX { return s.pool }

// WithTx rolls back on callback errors, cancellation, and panics. It deliberately
// does not retry: retrying a callback could repeat non-database side effects.
// A lost connection during COMMIT may leave the outcome unknown to the caller.
func (s *Store) WithTx(ctx context.Context, fn func(DBTX) error) error {
	return s.withTx(ctx, func(tx pgx.Tx) error { return fn(tx) })
}

// WithReadTx supplies one consistent database snapshot to repository reads.
func (s *Store) WithReadTx(ctx context.Context, fn func(DBTX) error) error {
	return s.transaction(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error { return fn(tx) })
}

func (s *Store) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return s.transaction(ctx, pgx.TxOptions{}, fn)
}

func (s *Store) transaction(ctx context.Context, options pgx.TxOptions, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL transaction: %w", err)
	}
	defer func() {
		// The request context may already be cancelled. Still release the
		// transaction/connection, including when fn panics.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL transaction: %w", err)
	}
	return nil
}
