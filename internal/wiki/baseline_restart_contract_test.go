package wiki

import (
	"context"
	"testing"

	"github.com/perber/wiki/internal/test_utils"
	wikipages "github.com/perber/wiki/internal/wiki/pages"
)

// Metadata-only edits intentionally leave history unchanged until the existing
// startup baseline pass. PostgreSQL must preserve this filesystem behavior.
func TestBaselineMetadataRestartContract(t *testing.T) {
	for _, backend := range []string{"filesystem", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			opts := &WikiOptions{StorageDir: t.TempDir(), AuthDisabled: true, EnableRevision: true}
			if backend == "postgres" {
				db, _ := test_utils.PostgresStore(t)
				if err := db.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				opts.Postgres = db
				observeNoSQLite(t, opts.StorageDir)
			}
			w, err := NewWiki(opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if w != nil {
					_ = w.Close()
				}
			})
			page := createPageForTest(t, w, "system", nil, "Metadata", "metadata", pageNodeKind())
			initial, err := w.revision.GetLatestRevision(page.ID)
			if err != nil || initial == nil {
				t.Fatalf("initial revision: %v", err)
			}
			out, err := wikipages.NewUpdatePageUseCase(w.tree, w.slug, w.newPageOrchestrator(), w.log, nil).Execute(context.Background(), wikipages.UpdatePageInput{
				UserID: "system", ID: page.ID, Version: page.Version(), Title: page.Title, Slug: page.Slug,
				Content: &page.Content, Tags: []string{"日本語"}, Properties: map[string]string{"owner": "example"},
			})
			if err != nil {
				t.Fatal(err)
			}
			before, err := w.revision.ListRevisions(page.ID)
			if err != nil || len(before) != 1 || before[0].ID != initial.ID {
				t.Fatalf("metadata-only edit history: %+v %v", before, err)
			}
			raw := out.Page.RawContent
			for restart := 0; restart < 2; restart++ {
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				w = nil
				w, err = NewWiki(opts)
				if err != nil {
					t.Fatal(err)
				}
				got, err := w.tree.GetPage(page.ID)
				if err != nil || got.RawContent != raw {
					t.Fatalf("restart changed content: %v", err)
				}
				history, err := w.revision.ListRevisions(page.ID)
				if err != nil || len(history) != 2 {
					t.Fatalf("restart %d history: %d %v", restart, len(history), err)
				}
				latest := history[0]
				if latest.Summary != "baseline" || latest.ContentHash != initial.ContentHash || latest.ExtraFrontmatterHash == initial.ExtraFrontmatterHash {
					t.Fatalf("unexpected baseline snapshot: %+v", latest)
				}
				if opts.Postgres != nil {
					var current string
					if err := opts.Postgres.DB().QueryRow(context.Background(), "SELECT current_revision_id FROM pages WHERE id=$1", page.ID).Scan(&current); err != nil || current != latest.ID {
						t.Fatalf("current revision: %s %v", current, err)
					}
				}
			}
		})
	}
}
