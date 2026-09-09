package transfer

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testArchive(t *testing.T, alter func(map[string][]byte)) string {
	t.Helper()
	now := time.Now().UTC()
	components := map[string][]byte{"database.dump": []byte("PGDMPfixture"), "backup-meta.json": []byte(`{"id":"fixture"}`), "assets/page/a.txt": []byte("asset bytes")}
	m := BackupManifest{Format: BackupFormat, FormatVersion: BackupFormatVersion, SchemaVersion: 5, SchemaChecksum: hashBytes([]byte("fixture-schema")), CreatedAt: now}
	for name, raw := range components {
		m.Files = append(m.Files, FileRecord{Path: name, SHA256: hashBytes(raw), Size: int64(len(raw)), ModifiedAt: now})
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	components[manifestName] = raw
	for _, root := range assetRoots {
		components[root+"/"] = nil
	}
	if alter != nil {
		alter(components)
	}
	path := filepath.Join(t.TempDir(), "backup.zip")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for name, raw := range components {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackupIntegrityRejectsCorruptIncompleteAndUnsafeArchives(t *testing.T) {
	for name, alter := range map[string]func(map[string][]byte){
		"corruption":        func(files map[string][]byte) { files["assets/page/a.txt"] = []byte("changed data") },
		"missing dump":      func(files map[string][]byte) { delete(files, "database.dump") },
		"missing asset":     func(files map[string][]byte) { delete(files, "assets/page/a.txt") },
		"missing directory": func(files map[string][]byte) { delete(files, "avatars/") },
		"unknown format":    func(files map[string][]byte) { files[manifestName] = []byte(`{"format":"other","format_version":1}`) },
		"traversal":         func(files map[string][]byte) { files["../outside"] = []byte("unexpected") },
		"unexpected file":   func(files map[string][]byte) { files["users.db"] = []byte("unexpected") },
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			archive := testArchive(t, alter)
			if _, _, err := StageBackup(context.Background(), archive, parent, 0); err == nil {
				t.Fatal("invalid backup accepted")
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed validation left extracted data")
			}
		})
	}
}

func TestBackupIntegrityPermissionsCancellationAndLimits(t *testing.T) {
	archive := testArchive(t, nil)
	parent := t.TempDir()
	stage, _, err := StageBackup(context.Background(), archive, parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(stage)
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatal("stage directory is not private")
	}
	file, err := os.Stat(filepath.Join(stage, "database.dump"))
	if err != nil || file.Mode().Perm() != 0600 {
		t.Fatal("dump is not private")
	}
	os.RemoveAll(stage)
	if _, _, err := StageBackup(context.Background(), archive, parent, 1); err == nil {
		t.Fatal("ignored extraction budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := StageBackup(ctx, archive, parent, 0); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestPGToolsDoNotDiscloseCredentialsOrSQL(t *testing.T) {
	script := filepath.Join(t.TempDir(), "client")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$PGPASSWORD\" >&2\nprintf 'secret SQL payload' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	err := (PGTools{Dump: script}).run(context.Background(), false, "postgres://user:password@localhost/db", nil, io.Discard)
	if err == nil || err.Error() != "PostgreSQL client failed; verify client availability, connection, permissions, versions and backup integrity" {
		t.Fatal("client error may disclose credentials or SQL")
	}
}
