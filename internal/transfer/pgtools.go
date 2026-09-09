package transfer

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

const TrustNotice = "Restore only backups created by LeafWiki or from a source you trust. PostgreSQL restore can execute SQL chosen by the backup creator. Checksums detect corruption; they do not establish that the backup is safe."

// PGTools uses standard PostgreSQL clients; overrides are for deployment paths
// and integration tests, never controlled by an uploaded backup.
type PGTools struct{ Dump, Restore string }

func (p PGTools) run(ctx context.Context, restore bool, dsn string, input io.Reader, output io.Writer, args ...string) error {
	command := p.Dump
	if command == "" {
		command = "pg_dump"
	}
	if restore {
		command = p.Restore
		if command == "" {
			command = "pg_restore"
		}
	}
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "PG") {
			env = append(env, v)
		}
	}
	if dsn != "" {
		u, err := url.Parse(dsn)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			return errors.New("backup/restore requires a PostgreSQL connection URI")
		}
		password := ""
		if u.User != nil {
			password, _ = u.User.Password()
			u.User = url.User(u.User.Username())
		}
		q := u.Query()
		if q.Has("password") {
			password = q.Get("password")
			q.Del("password")
		}
		u.RawQuery = q.Encode()
		env = append(env, "PGPASSWORD="+password, "PGAPPNAME=leafwiki-transfer")
		args = append(args, "--dbname="+u.String())
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	cmd.Stdin = input
	cmd.Stdout = output
	// Client errors can echo SQL, record values or connection parameters. Never
	// return stderr to the CLI/job logs. Exit failure remains explicit.
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("PostgreSQL client failed; verify client availability, connection, permissions, versions and backup integrity")
	}
	return nil
}
