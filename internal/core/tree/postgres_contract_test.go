package tree_test

import (
	"context"
	"errors"

	"log/slog"

	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/perber/wiki/internal/core/revision"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/storage/postgres"
	"github.com/perber/wiki/internal/test_utils"
	"github.com/perber/wiki/internal/wiki/pages"
	"github.com/perber/wiki/internal/wiki/pagesave"
)

func TestPageStorageContract(t *testing.T) {
	for _, backend := range []string{"filesystem", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			tr := tree.NewTreeService(dir)
			var db *postgres.Store
			var config postgres.Config
			if backend == "postgres" {
				db, config = test_utils.PostgresStore(t)
				if err := db.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				tr = tree.NewPostgresTreeService(dir, db)
			}
			if err := tr.LoadTree(); err != nil {
				t.Fatal(err)
			}
			rev := revision.NewService(dir, tr, nil, revision.ServiceOptions{MaxRevisions: 3, CoalesceWindow: time.Hour})
			orchestrator := pagesave.NewPageSaveOrchestrator(nil, pagesave.NewRevisionSideEffect(rev, nil, nil))
			create := pages.NewCreatePageUseCase(tr, tree.NewSlugService(), orchestrator, slog.Default(), nil)
			update := pages.NewUpdatePageUseCase(tr, tree.NewSlugService(), orchestrator, slog.Default(), nil)
			makePage := func(parent, title, slug string, kind tree.NodeKind) *tree.Page {
				t.Helper()
				out, err := create.Execute(context.Background(), pages.CreatePageInput{UserID: "alice", ParentID: &parent, Title: title, Slug: slug, Kind: &kind})
				if err != nil {
					t.Fatal(err)
				}
				return out.Page
			}
			edit := func(id, title, slug, content, author string, raw bool) {
				t.Helper()
				p, err := tr.GetPage(id)
				if err != nil {
					t.Fatal(err)
				}
				_, err = update.Execute(context.Background(), pages.UpdatePageInput{UserID: author, ID: id, Version: p.Version(), Title: title, Slug: slug, Content: &content, PreserveFrontmatter: raw})
				if err != nil {
					t.Fatal(err)
				}
			}
			section := makePage("root", "Section", "section", tree.NodeKindSection)
			if section.Kind != tree.NodeKindSection || section.Content != "" {
				t.Fatalf("section: %+v", section)
			}
			first := makePage(section.ID, "Duplicate", "one", tree.NodeKindPage)
			second := makePage(section.ID, "Duplicate", "two", tree.NodeKindPage)
			other := makePage("root", "Other", "other", tree.NodeKindSection)
			makePage(other.ID, "Duplicate", "one", tree.NodeKindPage)
			for _, slug := range []string{"one", "ONE"} {
				kind := tree.NodeKindPage
				if _, err := create.Execute(context.Background(), pages.CreatePageInput{UserID: "alice", ParentID: &section.ID, Title: "Conflict", Slug: slug, Kind: &kind}); err == nil {
					t.Fatal("accepted sibling slug collision")
				}
			}
			if err := tr.SortPages(section.ID, []string{second.ID, first.ID}); err != nil {
				t.Fatal(err)
			}
			parent, err := tr.GetPage(section.ID)
			if err != nil || parent.Children[0].ID != second.ID {
				t.Fatalf("order: %v", err)
			}
			before, err := rev.GetLatestRevision(first.ID)
			if err != nil {
				t.Fatal(err)
			}
			edit(first.ID, "Duplicate", "one", "---\ncustom:\n  nested: yes\ntags: [日本語]\n---\n本文一", "alice", true)
			coalesced, err := rev.GetLatestRevision(first.ID)
			if err != nil || coalesced.ID != before.ID {
				t.Fatalf("coalescing: %v", err)
			}
			snap, err := rev.GetRevisionSnapshot(first.ID, coalesced.ID)
			if err != nil || !strings.Contains(snap.Content, "本文一") {
				t.Fatalf("snapshot: %v", err)
			}
			raw, err := tr.ReadPageRaw(first.ID)
			if err != nil || !strings.Contains(raw, "custom:") || !strings.Contains(raw, first.ID) {
				t.Fatalf("frontmatter: %s %v", raw, err)
			}
			if _, err := tr.SetPinned(first.ID, tree.VersionUnchecked, true); err != nil {
				t.Fatal(err)
			}
			edit(first.ID, "Renamed", "renamed", "本文二", "bob", false)
			if err := tr.MoveNodeToPosition("bob", first.ID, other.ID, tree.VersionUnchecked, 0); err != nil {
				t.Fatal(err)
			}
			if err := rev.RestoreRevision(first.ID, coalesced.ID, "carol"); err != nil {
				t.Fatal(err)
			}
			restored, err := tr.GetPage(first.ID)
			if err != nil || restored.Title != "Duplicate" || restored.Slug != "renamed" || restored.Parent.ID != other.ID || !strings.Contains(restored.Content, "本文一") {
				t.Fatalf("restore: %+v %v", restored, err)
			}
			history, err := rev.ListRevisions(first.ID)
			if err != nil || len(history) != 3 || history[0].Type != revision.RevisionTypeRestore {
				t.Fatalf("history: %d %v", len(history), err)
			}
			edit(first.ID, "Duplicate", "renamed", "本文三", "dave", false)
			history, err = rev.ListRevisions(first.ID)
			if err != nil || len(history) != 3 {
				t.Fatalf("retention: %d %v", len(history), err)
			}
			page1, cursor, err := rev.ListRevisionsPage(first.ID, "", 1)
			if err != nil || len(page1) != 1 || cursor == "" {
				t.Fatalf("pagination: %v", err)
			}
			page2, _, err := rev.ListRevisionsPage(first.ID, cursor, 1)
			if err != nil || len(page2) != 1 || page1[0].ID == page2[0].ID {
				t.Fatalf("next page: %v", err)
			}

			assetPage := makePage("root", "Assets", "asset-page", tree.NodeKindPage)
			assetDir := filepath.Join(dir, "assets", assetPage.ID)
			if err := os.MkdirAll(assetDir, 0755); err != nil {
				t.Fatal(err)
			}
			assetPath := filepath.Join(assetDir, "sample.txt")
			if err := os.WriteFile(assetPath, []byte("original attachment"), 0644); err != nil {
				t.Fatal(err)
			}
			assetRevision, _, err := rev.RecordAssetChange(assetPage.ID, "alice", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(assetPath, []byte("changed attachment"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := rev.RecordAssetChange(assetPage.ID, "bob", ""); err != nil {
				t.Fatal(err)
			}
			if err := rev.RestoreRevision(assetPage.ID, assetRevision.ID, "carol"); err != nil {
				t.Fatal(err)
			}
			restoredAsset, err := os.ReadFile(assetPath)
			if err != nil || string(restoredAsset) != "original attachment" {
				t.Fatalf("asset restore: %s %v", restoredAsset, err)
			}
			// RestoreNode is also the existing-ID boundary needed by later import work.
			fixed, err := tr.RestoreNode("alice", "preserved-id", nil, "Preserved", "preserved", tree.NodeKindPage, "preserved body", first.Metadata)
			if err != nil || fixed.ID != "preserved-id" || !fixed.Metadata.CreatedAt.Equal(first.Metadata.CreatedAt) {
				t.Fatalf("preserved metadata: %+v %v", fixed, err)
			}
			convert := pages.NewConvertPageUseCase(tr, rev, slog.Default())
			if err := convert.Execute(context.Background(), pages.ConvertPageInput{UserID: "alice", ID: second.ID, Version: second.Version(), TargetKind: tree.NodeKindSection}); err != nil {
				t.Fatal(err)
			}
			converted, err := tr.GetPage(second.ID)
			if err != nil || converted.Kind != tree.NodeKindSection {
				t.Fatalf("convert: %v", err)
			}
			if err := convert.Execute(context.Background(), pages.ConvertPageInput{UserID: "alice", ID: second.ID, Version: converted.Version(), TargetKind: tree.NodeKindPage}); err != nil {
				t.Fatal(err)
			}
			if backend == "postgres" {
				// Failed history writes must roll back both raw Markdown and version tokens.
				beforeRaw, err := tr.ReadPageRaw(first.ID)
				if err != nil {
					t.Fatal(err)
				}
				beforeHistory, err := rev.ListRevisions(first.ID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.DB().Exec(context.Background(), `CREATE FUNCTION reject_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected history failure'; END $$;
CREATE TRIGGER reject_revision BEFORE INSERT OR UPDATE ON revisions FOR EACH ROW EXECUTE FUNCTION reject_revision()`)
				if err != nil {
					t.Fatal(err)
				}
				current, err := tr.GetPage(first.ID)
				if err != nil {
					t.Fatal(err)
				}
				failedBody := "must not commit"
				_, err = update.Execute(context.Background(), pages.UpdatePageInput{UserID: "eve", ID: first.ID, Version: current.Version(), Title: "Failed", Slug: current.Slug, Content: &failedBody})
				if err == nil {
					t.Fatal("revision failure was ignored")
				}

				if err := rev.RestoreRevision(first.ID, beforeHistory[1].ID, "eve"); err == nil {
					t.Fatal("restore ignored history failure")
				}
				afterRaw, err := tr.ReadPageRaw(first.ID)
				if err != nil || afterRaw != beforeRaw {
					t.Fatalf("non-atomic page update: %v", err)
				}
				afterHistory, err := rev.ListRevisions(first.ID)
				if err != nil || !reflect.DeepEqual(beforeHistory, afterHistory) {
					t.Fatalf("non-atomic history update: %v", err)
				}
				if _, err = db.DB().Exec(context.Background(), `DROP TRIGGER reject_revision ON revisions; DROP FUNCTION reject_revision()`); err != nil {
					t.Fatal(err)
				}
				// NULL Markdown represents a legacy section without an index file.
				if _, err = db.DB().Exec(context.Background(), `UPDATE pages SET content_markdown=NULL WHERE id=$1`, section.ID); err != nil {
					t.Fatal(err)
				}
				empty, err := tr.GetPage(section.ID)
				if err != nil || empty.Content != "" || empty.Kind != tree.NodeKindSection {
					t.Fatalf("contentless section: %v", err)
				}
				// Normal page operations must not materialize workspace Markdown/history JSON.
				for _, name := range []string{"root", "schema.json"} {
					if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
						t.Fatalf("unexpected filesystem source %s: %v", name, err)
					}
				}
				// A new pool/service cannot depend on the old tree or Markdown cache.
				reopened, err := postgres.Open(context.Background(), config)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				tr = tree.NewPostgresTreeService(t.TempDir(), reopened)
				persisted, err := tr.GetPage(first.ID)
				if err != nil || persisted.Content != "本文三" || !persisted.Pinned {
					t.Fatalf("restart: %+v %v", persisted, err)
				}
				if err := tr.Transact(context.Background(), func(local *tree.TreeService) error {
					content := "rollback"
					if err := local.UpdateNode("alice", first.ID, "Wrong", "wrong", &content, tree.VersionUnchecked, nil, nil, false); err != nil {
						return err
					}
					_, tx := local.TransactionDB()
					_, err := tx.Exec(context.Background(), "SELECT 1/0")
					return err
				}); err == nil {
					t.Fatal("expected rollback")
				}
				persisted, err = tr.GetPage(first.ID)
				if err != nil || persisted.Title != "Duplicate" || persisted.Content != "本文三" {
					t.Fatalf("rollback persisted: %+v %v", persisted, err)
				}
				var wg sync.WaitGroup
				results := make(chan error, 2)
				version := persisted.Version()
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						content := "concurrent"
						results <- tr.UpdateNode("alice", first.ID, "Duplicate", "renamed", &content, version, nil, nil, false)
					}()
				}
				wg.Wait()
				close(results)
				success := 0
				for err := range results {
					if err == nil {
						success++
					}
				}
				if success != 1 {
					t.Fatalf("concurrent successes: %d", success)
				}
				results = make(chan error, 2)
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						kind := tree.NodeKindPage
						_, err := tr.CreateNode("alice", &other.ID, "Race", "race", &kind)
						results <- err
					}()
				}
				wg.Wait()
				close(results)
				success = 0
				for err := range results {
					if err == nil {
						success++
					}
				}
				if success != 1 {
					t.Fatalf("concurrent slug successes: %d", success)
				}

				compete := func(op func() error) {
					t.Helper()
					results := make(chan error, 2)
					for i := 0; i < 2; i++ {
						wg.Add(1)
						go func() { defer wg.Done(); results <- op() }()
					}
					wg.Wait()
					close(results)
					success := 0
					for err := range results {
						if err == nil {
							success++
						} else if !errors.Is(err, tree.ErrVersionConflict) {
							t.Fatalf("unexpected concurrency error: %v", err)
						}
					}
					if success != 1 {
						t.Fatalf("expected one winner, got %d", success)
					}
				}
				renameBefore, err := tr.GetPage(first.ID)
				if err != nil {
					t.Fatal(err)
				}
				compete(func() error {
					return tr.UpdateNode("alice", first.ID, renameBefore.Title, "concurrent-rename", nil, renameBefore.Version(), nil, nil, false)
				})
				moveBefore, err := tr.GetPage(first.ID)
				if err != nil {
					t.Fatal(err)
				}
				compete(func() error { return tr.MoveNodeToPosition("alice", first.ID, section.ID, moveBefore.Version(), 0) })
				ordered, err := tr.GetPage(section.ID)
				if err != nil || ordered.Children[0].ID != first.ID {
					t.Fatalf("concurrent move order: %v", err)
				}
				// Kill an actual backend during a page write, then use the same pool again.
				err = tr.Transact(context.Background(), func(local *tree.TreeService) error {
					ctx, tx := local.TransactionDB()
					var pid int
					if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
						return err
					}
					if _, err := db.DB().Exec(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
						return err
					}
					content := "lost"
					return local.UpdateNode("alice", first.ID, "Lost", "lost", &content, tree.VersionUnchecked, nil, nil, false)
				})
				if err == nil {
					t.Fatal("terminated backend succeeded")
				}
				recovered, err := tr.GetPage(first.ID)
				if err != nil || recovered.Content != "concurrent" {
					t.Fatalf("connection recovery: %+v %v", recovered, err)
				}
				content := "recovered"
				if err := tr.UpdateNode("alice", first.ID, recovered.Title, recovered.Slug, &content, recovered.Version(), nil, nil, false); err != nil {
					t.Fatal(err)
				}

			}
			if err := tr.DeleteNode("alice", second.ID, false, tree.VersionUnchecked); err != nil {
				t.Fatal(err)
			}
			if _, err := tr.GetPage(second.ID); err == nil {
				t.Fatal("deleted page returned")
			}
		})
	}
}
