package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
)

// SQLDB adapts the existing pool for repositories using database/sql. It creates
// no independent pool: pgx owns idle connections. Closing this handle does not
// close the shared Store. No schema or legacy files are opened here.
func (s *Store) SQLDB() *sql.DB { return stdlib.OpenDBFromPool(s.pool) }

// SQLQueries is the common query surface of a database/sql DB and transaction.
// Both PostgreSQL and the legacy SQLite reader accept numbered $n parameters.
type SQLQueries interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func WithSQLTx(db *sql.DB, fn func(*sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
