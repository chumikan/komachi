package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPostgresDoesNotSnapshotLegacyCredentials(t *testing.T) {
	dir := t.TempDir()
	users := filepath.Join(dir, "users.db")
	if err := os.WriteFile(users, []byte("legacy untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := createSnapshot(context.Background(), Config{Postgres: true, UsersDBPath: users, BackupsDir: filepath.Join(dir, "backups")})
	if err == nil {
		t.Fatal("created misleading PostgreSQL backup")
	}
	data, err := os.ReadFile(users)
	if err != nil || string(data) != "legacy untouched" {
		t.Fatal("legacy credentials modified")
	}
	if _, err := os.Stat(filepath.Join(dir, "backups")); !os.IsNotExist(err) {
		t.Fatal("snapshot started before backend check")
	}
}
