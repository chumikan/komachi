package transfer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// withSQLiteCopy never opens the source through SQLite. A read-only SQLite
// connection can still need a writable WAL shared-memory file, and immutable=1
// ignores committed WAL records. Copy the DB and WAL instead, then let SQLite
// recover only the disposable private copy. The caller must stop the legacy
// instance and check its source inventory before and after the whole transfer.
// Copying files is not a substitute for a coherent snapshot of a running writer.
func withSQLiteCopy(ctx context.Context, source string, fn func(*sql.DB) error) error {
	tmp, err := os.MkdirTemp("", "leafwiki-legacy-sqlite-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	copyPath := filepath.Join(tmp, "source.db")
	for _, suffix := range []string{"", "-wal", "-journal"} {
		if err := copyRegular(ctx, source+suffix, copyPath+suffix); err != nil {
			if suffix != "" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
	}
	// No source shm is necessary: SQLite rebuilds it from the copied WAL.
	u := url.URL{Scheme: "file", Path: copyPath}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return fmt.Errorf("cannot open private SQLite copy")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("cannot validate SQLite copy")
	}
	if integrity != "ok" {
		// SQLite diagnostics may contain record values. Do not echo them.
		return fmt.Errorf("SQLite integrity check failed")
	}
	return fn(db)
}

func copyRegular(ctx context.Context, source, target string) error {
	st, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("source must be a regular file")
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(st, opened) {
		return fmt.Errorf("source changed during copy")
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(out, contextReader{ctx: ctx, reader: in})
	if readErr == nil {
		readErr = out.Sync()
	}
	closeErr := out.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	return os.Chtimes(target, st.ModTime(), st.ModTime())
}
