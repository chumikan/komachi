// Package favorites stores each user's private set of favorited pages.
// Unlike tags/links/properties/search, this data is not derived from the
// filesystem tree and must never be touched by resync (see ADR-0001).
package favorites

import (
	"database/sql"
	"fmt"
	"github.com/perber/wiki/internal/storage/postgres"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/perber/wiki/internal/core/shared"
	sharederrors "github.com/perber/wiki/internal/core/shared/errors"
	"github.com/perber/wiki/internal/core/shared/sqliteutil"
	_ "modernc.org/sqlite"
)

const logCloseRowsFailed = "could not close rows"

// ErrCodeFavoritesStoreUnavailable identifies errFavoritesStoreUnavailable's
// LocalizedError. Mirrors ErrCodeAPIKeyStoreUnavailable.
const ErrCodeFavoritesStoreUnavailable = "favorites_store_unavailable"

// errFavoritesStoreUnavailable is returned while the store is suspended for
// an in-progress live restore (see FavoritesStore.PauseForSwap). Mirrors
// errAPIKeyStoreUnavailable.
func errFavoritesStoreUnavailable() error {
	return sharederrors.NewLocalizedError(
		ErrCodeFavoritesStoreUnavailable,
		"The server is restoring from a backup — please try again in a moment",
		"favorites store is suspended for an in-progress restore",
		nil,
	)
}

type FavoritesStore struct {
	pg *postgres.Store
	tx *sql.Tx
	mu sync.Mutex
	db *sql.DB
	// suspended is set by PauseForSwap and makes withDB refuse to serve
	// queries against a possibly-stale db handle until Replace reopens it.
	suspended bool
	log       *slog.Logger
}

func NewFavoritesStore(storageDir string, log *slog.Logger) (*FavoritesStore, error) {
	normalized := filepath.FromSlash(strings.ReplaceAll(storageDir, `\`, `/`))
	dbPath := filepath.Join(normalized, "favorites.db")

	s := &FavoritesStore{log: log}
	err := sqliteutil.RetryOnCorruption(dbPath, func() error {
		db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
		if err != nil {
			return fmt.Errorf("failed to open favorites database: %w", err)
		}
		s.db = db
		if err := s.ensureSchema(); err != nil {
			_ = db.Close()
			s.db = nil
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FavoritesStore) ensureSchema() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS favorites (
			user_id    TEXT NOT NULL,
			page_id    TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, page_id)
		);
		CREATE INDEX IF NOT EXISTS favorites_user_id_idx ON favorites(user_id);
	`)
	return err
}

// withDB runs fn against the current db handle, or returns
// errFavoritesStoreUnavailable if the store is suspended for an in-progress
// restore. Mirrors APIKeyStore.withDB.
func (s *FavoritesStore) withDB(fn func(db *sql.DB) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.suspended || s.db == nil {
		return errFavoritesStoreUnavailable()
	}
	return fn(s.db)
}

// Add favorites pageID for userID. Idempotent — favoriting an already-favorited page is a no-op.
func (s *FavoritesStore) Add(userID, pageID string) error {
	err := s.withQueries(func(db postgres.SQLQueries) error {
		_, err := db.Exec(
			`INSERT INTO favorites (user_id, page_id, created_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			userID, pageID, time.Now().UTC(),
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to add favorite for user %s, page %s: %w", userID, pageID, err)
	}
	return nil
}

// Remove un-favorites pageID for userID. Idempotent — removing a non-favorited page is a no-op.
func (s *FavoritesStore) Remove(userID, pageID string) error {
	err := s.withQueries(func(db postgres.SQLQueries) error {
		_, err := db.Exec(`DELETE FROM favorites WHERE user_id = $1 AND page_id = $2`, userID, pageID)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to remove favorite for user %s, page %s: %w", userID, pageID, err)
	}
	return nil
}

// ListPageIDsForUser returns the page IDs favorited by userID, most recently favorited first.
func (s *FavoritesStore) ListPageIDsForUser(userID string) ([]string, error) {
	var pageIDs []string
	err := s.withQueries(func(db postgres.SQLQueries) error {
		order := `SELECT page_id FROM favorites WHERE user_id = $1 ORDER BY created_at DESC`
		if s.pg != nil {
			order += `, created_at_submicro DESC`
		}
		rows, err := db.Query(
			order,
			userID,
		)
		if err != nil {
			return err
		}
		defer shared.LogClose(rows.Close, logCloseRowsFailed)

		for rows.Next() {
			var pageID string
			if err := rows.Scan(&pageID); err != nil {
				return err
			}
			pageIDs = append(pageIDs, pageID)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return pageIDs, nil
}

// DeleteAllForPage removes every user's favorite of pageID. Called on page delete.
func (s *FavoritesStore) DeleteAllForPage(pageID string) error {
	err := s.withQueries(func(db postgres.SQLQueries) error {
		_, err := db.Exec(`DELETE FROM favorites WHERE page_id = $1`, pageID)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to delete favorites for page %s: %w", pageID, err)
	}
	return nil
}

// DeleteAllForUser removes every favorite belonging to userID. Called on user delete.
func (s *FavoritesStore) DeleteAllForUser(userID string) error {
	err := s.withQueries(func(db postgres.SQLQueries) error {
		_, err := db.Exec(`DELETE FROM favorites WHERE user_id = $1`, userID)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to delete favorites for user %s: %w", userID, err)
	}
	return nil
}

// PauseForSwap closes the current *sql.DB connection and marks the store
// suspended so withDB refuses to serve queries against a possibly-stale
// handle, releasing any OS-level file lock on favorites.db before a live
// restore renames it. Idempotent: a second call is a safe no-op. Mirrors
// APIKeyStore.suspend.
func (s *FavoritesStore) PauseForSwap() error {
	if s.pg != nil {
		return fmt.Errorf("legacy workspace restore cannot replace PostgreSQL FavoritesStore; PostgreSQL restore is a separate operation")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.suspended = true
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Replace reopens favorites.db from storageDir and swaps the new connection
// in, closing the previous one afterward — used by a live restore once
// favorites.db has been swapped in on disk (or left untouched, if this
// snapshot didn't capture one). Preserves this FavoritesStore's identity: no
// caller needs to be told about the new connection, they already hold this
// pointer. Mirrors APIKeyService.Replace, adapted for the fact that
// FavoritesStore has no separate service-layer indirection to swap.
func (s *FavoritesStore) Replace(storageDir string) error {
	if s.pg != nil {
		return fmt.Errorf("legacy workspace restore cannot replace PostgreSQL FavoritesStore; PostgreSQL restore is a separate operation")
	}

	fresh, err := NewFavoritesStore(storageDir, s.log)
	if err != nil {
		return err
	}

	s.mu.Lock()
	old := s.db
	s.db = fresh.db
	s.suspended = false
	s.mu.Unlock()

	if old != nil {
		if err := old.Close(); err != nil && s.log != nil {
			s.log.Warn("failed to close previous favorites store after restore", "error", err)
		}
	}
	return nil
}

func (s *FavoritesStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		if err := s.db.Close(); err != nil {
			return err
		}
		s.db = nil
	}
	return nil
}

func (s *FavoritesStore) withQueries(fn func(postgres.SQLQueries) error) error {
	if s.tx != nil {
		return fn(s.tx)
	}
	return s.withDB(func(db *sql.DB) error { return fn(db) })
}

func NewPostgresFavoritesStore(pg *postgres.Store, log *slog.Logger) *FavoritesStore {
	return &FavoritesStore{pg: pg, db: pg.SQLDB(), log: log}
}

func (s *FavoritesStore) UsesPostgres() bool { return s.pg != nil }
