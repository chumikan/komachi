package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/perber/wiki/internal/storage/postgres"
)

func NewPostgresUserStore(pg *postgres.Store) *UserStore { return &UserStore{pg: pg, db: pg.SQLDB()} }
func NewPostgresAPIKeyStore(pg *postgres.Store) *APIKeyStore {
	return &APIKeyStore{pg: pg, db: pg.SQLDB()}
}
func NewPostgresSessionStore(pg *postgres.Store) *SessionStore {
	ctx, cancel := context.WithCancel(context.Background())
	s := &SessionStore{pg: pg, db: pg.SQLDB(), cancel: cancel, done: make(chan struct{}), log: slog.Default().With("component", "SessionStore")}
	go cleanupPostgres(ctx, s.done, s.log, s.CleanupExpiredSessions)
	return s
}
func NewPostgresEmailTokenStore(pg *postgres.Store) *EmailTokenStore {
	ctx, cancel := context.WithCancel(context.Background())
	s := &EmailTokenStore{pg: pg, db: pg.SQLDB(), cancel: cancel, done: make(chan struct{}), log: slog.Default().With("component", "EmailTokenStore")}
	go cleanupPostgres(ctx, s.done, s.log, s.CleanupExpired)
	return s
}
func cleanupPostgres(ctx context.Context, done chan struct{}, log *slog.Logger, cleanup func() error) {
	defer close(done)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := cleanup(); err != nil {
				log.Warn("failed to cleanup expired records")
			}
		}
	}
}

func uniqueConstraint(err error, name string) bool {
	var e *pgconn.PgError
	return errors.As(err, &e) && e.Code == "23505" && e.ConstraintName == name
}
func (s *UserStore) bind(tx *sql.Tx) *UserStore { return &UserStore{pg: s.pg, db: s.db, tx: tx} }
func (s *UserService) bind(tx *sql.Tx) *UserService {
	return &UserService{store: s.store.bind(tx), log: s.log, editorLimit: s.editorLimit}
}
func (s *UserService) postgresTransaction(fn func(*UserService) error) error {
	if err := s.store.Connect(); err != nil {
		return err
	}
	return postgres.WithSQLTx(s.store.db, func(tx *sql.Tx) error {
		// Preserve SQLite's serialized writes for last-admin/editor-limit decisions.
		if _, err := tx.Exec("LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return err
		}
		return fn(s.bind(tx))
	})
}
func (a *AuthService) bind(tx *sql.Tx) *AuthService {
	users := a.users().bind(tx)
	sessions := *a.sessions
	sessions.sessionStore = &SessionStore{pg: a.sessions.sessionStore.pg, db: a.sessions.sessionStore.db, tx: tx}
	sessions.resolveUser = users.GetUserByID
	return &AuthService{userService: users, sessions: &sessions, totp: a.totp, attempts: a.attempts, dummyHash: a.dummyHash, log: a.log}
}
func (a *AuthService) postgresTransaction(fn func(*AuthService) error) error {
	return a.users().postgresTransaction(func(users *UserService) error { return fn(a.bind(users.store.tx)) })
}
func (s *EmailTokenService) postgresTransaction(fn func(*EmailTokenService) error) error {
	return s.users.postgresTransaction(func(users *UserService) error {
		tx := users.store.tx
		local := &EmailTokenService{tokens: &EmailTokenStore{pg: s.tokens.pg, db: s.tokens.db, tx: tx}, users: users, auth: s.auth.bind(tx), log: s.log}
		return fn(local)
	})
}
