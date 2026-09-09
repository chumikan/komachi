package transfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInventoryIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.md")
	raw := []byte("---\nleafwiki_id: original\n---\n本文\n")
	if err := os.WriteFile(path, raw, 0400); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := Inventory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].SHA256 != hashBytes(raw) {
		t.Fatalf("inventory: %+v", records)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		t.Fatal("inspection modified source")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatal("inspection rewrote Markdown")
	}
}
func TestInventoryRejectsSymlinksAndCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "outside")); err != nil {
		t.Skip(err)
	}
	if _, err := Inventory(context.Background(), dir); err == nil {
		t.Fatal("followed symlink")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inventory(ctx, dir); err == nil {
		t.Fatal("ignored cancellation")
	}
}
