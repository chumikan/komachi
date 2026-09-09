package favorites

import (
	"context"
	"github.com/perber/wiki/internal/test_utils"
	"log/slog"
	"testing"
)

func TestFavoritesStorageContract(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var s *FavoritesStore
			if backend == "postgres" {
				pg, _ := test_utils.PostgresStore(t)
				if err := pg.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				s = NewPostgresFavoritesStore(pg, slog.Default())
			} else {
				var err error
				s, err = NewFavoritesStore(t.TempDir(), slog.Default())
				if err != nil {
					t.Fatal(err)
				}
			}
			defer s.Close()
			// The existing store permits system actors and missing/deleted page IDs.
			for _, id := range []string{"first", "second", "second"} {
				if err := s.Add("system", id); err != nil {
					t.Fatal(err)
				}
			}
			ids, err := s.ListPageIDsForUser("system")
			if err != nil || len(ids) != 2 || ids[0] != "second" {
				t.Fatal("idempotency or ordering changed")
			}
			if err := s.Remove("system", "missing"); err != nil {
				t.Fatal(err)
			}
			if err := s.DeleteAllForPage("first"); err != nil {
				t.Fatal(err)
			}
			ids, err = s.ListPageIDsForUser("system")
			if err != nil || len(ids) != 1 {
				t.Fatal("page cleanup failed")
			}
			if err := s.DeleteAllForUser("system"); err != nil {
				t.Fatal(err)
			}
			ids, err = s.ListPageIDsForUser("system")
			if err != nil || len(ids) != 0 {
				t.Fatal("user cleanup failed")
			}
		})
	}
}
