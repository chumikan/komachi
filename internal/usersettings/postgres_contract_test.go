package usersettings

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/perber/wiki/internal/test_utils"
)

func TestUserSettingsStorageContract(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var store, other *UserSettingsStore
			if backend == "postgres" {
				pg, _ := test_utils.PostgresStore(t)
				if err := pg.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				store = NewPostgresUserSettingsStore(pg, slog.Default())
				other = NewPostgresUserSettingsStore(pg, slog.Default())
				defer other.Close()
			} else {
				var err error
				store, err = NewUserSettingsStore(t.TempDir(), slog.Default())
				if err != nil {
					t.Fatal(err)
				}
				other = store
			}
			defer store.Close()
			defaults, err := store.Get("existing-id")
			if err != nil || defaults.Language != "en" || !defaults.AutoSave || defaults.DateFormat != "locale" || defaults.TimeFormat != "locale" {
				t.Fatal("defaults changed")
			}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, err := store.UpdateAtomic("existing-id", func(s *UserSettings) { s.Language = "ja"; s.UpdatedAt = time.Now().UTC() })
				results <- err
			}()
			go func() {
				defer wg.Done()
				_, err := other.UpdateAtomic("existing-id", func(s *UserSettings) {
					s.AutoSave = false
					s.DateFormat = "yyyy-mm-dd"
					s.TimeFormat = "24h"
					s.UpdatedAt = time.Now().UTC()
				})
				results <- err
			}()
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Fatal(err)
				}
			}
			current, err := store.Get("existing-id")
			if err != nil || current.Language != "ja" || current.AutoSave || current.DateFormat != "yyyy-mm-dd" || current.TimeFormat != "24h" {
				t.Fatal("concurrent update lost fields")
			}
			if err := store.DeleteAllForUser("existing-id"); err != nil {
				t.Fatal(err)
			}
			current, err = store.Get("existing-id")
			if err != nil || current.Language != "en" {
				t.Fatal("delete did not reset defaults")
			}
		})
	}
}
