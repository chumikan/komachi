package transfer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteCopyPreservesCommittedWALWithoutSourceWrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "users.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
		CREATE TABLE users(id TEXT PRIMARY KEY, password TEXT);
		INSERT INTO users VALUES('original-id', 'opaque-hash')`); err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(source + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatal("fixture must contain an uncheckpointed committed WAL")
	}
	before, err := Inventory(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	err = withSQLiteCopy(ctx, source, func(copyDB *sql.DB) error {
		var id, hash string
		if err := copyDB.QueryRow(`SELECT id,password FROM users`).Scan(&id, &hash); err != nil {
			return err
		}
		if id != "original-id" || hash != "opaque-hash" {
			t.Fatal("lost opaque data committed to WAL")
		}
		if _, err := copyDB.Exec(`DELETE FROM users`); err == nil {
			t.Fatal("private inspection connection permits data mutation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := Inventory(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if inventoryHash(before) != inventoryHash(after) {
		t.Fatal("inspection modified source DB/WAL/shm")
	}
}

func TestSQLiteCopyRejectsCorruptionAndMissingSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "users.db")
	called := false
	visit := func(*sql.DB) error { called = true; return nil }
	if err := withSQLiteCopy(context.Background(), source, visit); err == nil {
		t.Fatal("accepted missing DB")
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("created missing source DB")
	}
	if err := os.WriteFile(source, []byte("secret-bearing-corrupt-content"), 0400); err != nil {
		t.Fatal(err)
	}
	err := withSQLiteCopy(context.Background(), source, visit)
	if err == nil || strings.Contains(err.Error(), "secret-bearing") || called {
		t.Fatal("corrupt DB must fail before processing without disclosing contents")
	}
}

func TestCopyRegularCancellationAndPermissions(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("opaque"), 0400); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := copyRegular(context.Background(), source, target); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(target)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("secret-bearing copy must have mode 0600")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyRegular(ctx, source, filepath.Join(t.TempDir(), "cancelled")); err == nil {
		t.Fatal("ignored cancellation")
	}
}
