package branding

import (
	"context"
	"github.com/perber/wiki/internal/test_utils"
	"testing"
)

func TestBrandingStorageContract(t *testing.T) {
	for _, backend := range []string{"json", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			var open func() (*BrandingService, error)
			if backend == "postgres" {
				pg, _ := test_utils.PostgresStore(t)
				if err := pg.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				open = func() (*BrandingService, error) { return NewPostgresBrandingService(dir, pg) }
			} else {
				open = func() (*BrandingService, error) { return NewBrandingService(dir) }
			}
			s, err := open()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateBranding("  日本語Wiki  "); err != nil {
				t.Fatal(err)
			}
			reopened, err := open()
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := reopened.GetBranding()
			if err != nil || cfg.SiteName != "日本語Wiki" {
				t.Fatal("branding not retained")
			}
			if err := s.UpdateBranding(""); err == nil {
				t.Fatal("accepted empty name")
			}
		})
	}
}
