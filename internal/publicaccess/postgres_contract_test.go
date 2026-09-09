package publicaccess

import (
	"context"
	"github.com/perber/wiki/internal/test_utils"
	"testing"
)

func TestPublicAccessStorageContract(t *testing.T) {
	for _, backend := range []string{"json", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var open func() (*Service, error)
			if backend == "postgres" {
				pg, _ := test_utils.PostgresStore(t)
				if err := pg.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				open = func() (*Service, error) { return NewPostgresSettingsManaged(pg) }
			} else {
				dir := t.TempDir()
				open = func() (*Service, error) { return NewSettingsManaged(dir) }
			}
			s, err := open()
			if err != nil || s.Enabled() || s.EnvManaged() {
				t.Fatalf("defaults: %v", err)
			}
			if err := s.SetEnabled(true); err != nil {
				t.Fatal(err)
			}
			reopened, err := open()
			if err != nil || !reopened.Enabled() {
				t.Fatal("setting not retained")
			}
			if err := s.SetEnabled(false); err != nil {
				t.Fatal(err)
			}
			if err := reopened.Reload(); err != nil || reopened.Enabled() {
				t.Fatal("reload failed")
			}
			fixed := NewEnvManaged(true)
			if err := fixed.SetEnabled(false); err == nil || !fixed.Enabled() {
				t.Fatal("override changed")
			}
		})
	}
}
