package tree

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/perber/wiki/internal/storage/postgres"
)

func NewPostgresTreeService(storageDir string, db *postgres.Store) *TreeService {
	t := NewTreeService(storageDir)
	t.pg = db
	return t
}

// UsesPostgres distinguishes the outer service from a transaction-bound copy.
func (t *TreeService) UsesPostgres() bool { return t.pg != nil }
func (t *TreeService) TransactionDB() (context.Context, postgres.DBTX) {
	if s, ok := t.store.(*postgresNodeStore); ok {
		return s.ctx, s.db
	}
	return nil, nil
}

// Transact runs existing tree operations on a fresh database snapshot. The root
// row replaces the existing process-wide mutation mutex across connections.
// This deliberately preserves serialized structural writes; no cache is authoritative.
func (t *TreeService) Transact(ctx context.Context, fn func(*TreeService) error) error {
	return t.databaseScope(ctx, true, fn)
}
func (t *TreeService) ReadTransaction(ctx context.Context, fn func(*TreeService) error) error {
	return t.databaseScope(ctx, false, fn)
}
func (t *TreeService) databaseScope(ctx context.Context, write bool, fn func(*TreeService) error) error {
	if t.pg == nil {
		return fn(t)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	run := t.pg.WithReadTx
	if write {
		run = t.pg.WithTx
	}
	err := run(ctx, func(db postgres.DBTX) error {
		if write {
			if _, e := db.Exec(ctx, `SELECT id FROM pages WHERE id='root' FOR UPDATE`); e != nil {
				return e
			}
		}
		local := NewTreeService(t.storageDir)
		s := &postgresNodeStore{NodeStore: NewNodeStore(t.storageDir), db: db, ctx: ctx}
		local.store = s
		root, e := s.load()
		if e != nil {
			return e
		}
		local.tree = root
		local.rebuildIndexesLocked()
		return fn(local)
	})
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" && pgerr.ConstraintName == "pages_sibling_slug" {
		return errors.Join(ErrPageAlreadyExists, err)
	}
	return err
}

func pgRead[T any](t *TreeService, fn func(*TreeService) (T, error)) (T, error) {
	var out T
	err := t.ReadTransaction(context.Background(), func(local *TreeService) error { var e error; out, e = fn(local); return e })
	return out, err
}
func pgWrite[T any](t *TreeService, fn func(*TreeService) (T, error)) (T, error) {
	var out T
	err := t.Transact(context.Background(), func(local *TreeService) error { var e error; out, e = fn(local); return e })
	return out, err
}
func pgReadValue[T any](t *TreeService, fn func(*TreeService) T) T {
	out, e := pgRead(t, func(local *TreeService) (T, error) { return fn(local), nil })
	if e != nil {
		t.log.Error("read PostgreSQL tree", "error", e)
	}
	return out
}
