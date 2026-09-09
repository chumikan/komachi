package links

import (
	"context"
	"github.com/perber/wiki/internal/test_utils"
	"testing"
)

func TestPostgresStorageContract(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var store *LinksStore
			var err error
			if backend == "sqlite" {
				store, err = NewLinksStore(t.TempDir())
			} else {
				db, _ := test_utils.PostgresStore(t)
				if err = db.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				store, err = NewPostgresLinksStore(db)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddLinks("source", "Source", []TargetLink{{TargetPageID: "target", TargetPagePath: "target"}, {TargetPagePath: "missing", Broken: true}}); err != nil {
				t.Fatal(err)
			}
			back, err := store.GetBacklinksForPage("target")
			if err != nil || len(back) != 1 {
				t.Fatalf("backlinks: %+v %v", back, err)
			}
			if err := store.MarkIncomingLinksBroken("target"); err != nil {
				t.Fatal(err)
			}
			back, err = store.GetBacklinksForPage("target")
			if err != nil || len(back) != 0 {
				t.Fatalf("broken: %+v %v", back, err)
			}
			if err := store.HealLinksForPath("target", "target"); err != nil {
				t.Fatal(err)
			}
			out, err := store.GetOutgoingLinksForPages([]string{"source"})
			if err != nil || len(out["source"]) != 2 {
				t.Fatalf("outgoing: %+v %v", out, err)
			}
			if err := store.ReplaceLinksAndHeal([]PageLinkUpdate{{FromPageID: "source", FromTitle: "Renamed", ToPath: "source", Targets: []TargetLink{{TargetPageID: "target", TargetPagePath: "target"}}}}); err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteOutgoingLinks("source"); err != nil {
				t.Fatal(err)
			}
			out, err = store.GetOutgoingLinksForPages([]string{"source"})
			if err != nil || len(out["source"]) != 0 {
				t.Fatalf("delete: %+v %v", out, err)
			}
		})
	}
}
