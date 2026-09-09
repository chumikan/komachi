package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/perber/wiki/internal/transfer"
)

func createPostgresSnapshot(ctx context.Context, cfg Config) (string, error) {
	if cfg.Database == nil || cfg.DatabaseURL == "" || cfg.DataDir == "" || cfg.Freeze == nil {
		return "", errors.New("PostgreSQL snapshot requires database, data directory and a write gate")
	}
	release, err := cfg.Freeze(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if err := os.MkdirAll(cfg.BackupsDir, 0700); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	id, err := uniqueSnapshotID(cfg.BackupsDir, now)
	if err != nil {
		return "", err
	}
	path := filepath.Join(cfg.BackupsDir, id+".zip")
	if err := transfer.CreateBackup(ctx, cfg.Database, cfg.DatabaseURL, cfg.DataDir, path, id, cfg.WikiVersion, cfg.PGTools); err != nil {
		return "", err
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if err := writeJSONFile(filepath.Join(cfg.BackupsDir, id+".json"), SnapshotEntry{ID: id, CreatedAt: now, SizeBytes: st.Size()}); err != nil {
		return "", err
	}
	return id, nil
}
