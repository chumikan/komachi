package wiki

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"
)

var observedPostgresWorkspaces sync.Map

func init() {
	// Installed before tests start. Count actual driver connections, including
	// transient opens which would not necessarily leave a .db file behind.
	sqlite.RegisterConnectionHook(func(_ sqlite.ExecQuerierContext, dsn string) error {
		observedPostgresWorkspaces.Range(func(key, value any) bool {
			if strings.Contains(dsn, key.(string)) {
				value.(*atomic.Int64).Add(1)
			}
			return true
		})
		return nil
	})
}

func observeNoSQLite(t *testing.T, dir string) {
	t.Helper()
	count := new(atomic.Int64)
	observedPostgresWorkspaces.Store(dir, count)
	t.Cleanup(func() {
		observedPostgresWorkspaces.Delete(dir)
		if count.Load() != 0 {
			t.Errorf("PostgreSQL runtime opened %d SQLite connections", count.Load())
		}
	})
}
