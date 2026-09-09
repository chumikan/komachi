package wiki

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/test_utils"
	wikipages "github.com/perber/wiki/internal/wiki/pages"
	"github.com/perber/wiki/internal/wiki/pagesave"
	wikirevisions "github.com/perber/wiki/internal/wiki/revisions"
	wikisearch "github.com/perber/wiki/internal/wiki/search"
)

func newDerivedWiki(t *testing.T) *Wiki {
	t.Helper()
	db, _ := test_utils.PostgresStore(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	w, err := NewWiki(&WikiOptions{Postgres: db, StorageDir: t.TempDir(), AuthDisabled: true, EnableRevision: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	w.reloadWG.Wait()
	return w
}

func updateDerivedPage(t *testing.T, w *Wiki, page *tree.Page, body string) *tree.Page {
	t.Helper()
	current, err := w.tree.GetPage(page.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := wikipages.NewUpdatePageUseCase(w.tree, w.slug, w.newPageOrchestrator(), w.log, nil).Execute(context.Background(), wikipages.UpdatePageInput{
		UserID: "system", ID: page.ID, Title: page.Title, Slug: page.Slug, Version: current.Version(), Content: &body, PreserveFrontmatter: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Page
}
func derivedEffects(w *Wiki) *pagesave.PageSaveOrchestrator {
	return pagesave.NewPageSaveOrchestrator(nil,
		pagesave.NewLinkIndexSideEffect(w.links, w.log, nil),
		pagesave.NewTagsSideEffect(w.tags, w.log, nil),
		pagesave.NewPropertiesSideEffect(w.props, w.log, nil),
		pagesave.NewSearchIndexSideEffect(w.searchIndex, w.tree, w.log, nil))
}
func assertDerivedTerm(t *testing.T, w *Wiki, pageID, term string, count int) {
	t.Helper()
	result, err := w.searchIndex.Search(term, []string{pageID}, 0, 10)
	if err != nil || result.Count != count {
		t.Fatalf("search %q: %+v %v", term, result, err)
	}
}

func TestPostgresDerivedLifecycle(t *testing.T) {
	w := newDerivedWiki(t)
	target := createPageForTest(t, w, "system", nil, "Target", "target", pageNodeKind())
	page := createPageForTest(t, w, "system", nil, "Source", "source", pageNodeKind())
	old := updateDerivedPage(t, w, page, "---\ntags: [old]\nproject: old\n---\n以前の本文 [[Target]]")
	history, err := w.revision.ListRevisions(page.ID)
	if err != nil || len(history) == 0 {
		t.Fatal(err)
	}
	revisionID := history[0].ID
	current := updateDerivedPage(t, w, page, "---\ntags: [new]\nproject: new\n---\n最新の本文")
	// A delayed update/rebuild must not replace the current projections.
	derivedEffects(w).Run(pagesave.PageSaveEvent{Operation: pagesave.PageOperationUpdate, After: old})
	assertDerivedTerm(t, w, page.ID, "最新", 1)
	assertDerivedTerm(t, w, page.ID, "以前", 0)
	tags, err := w.tags.GetTagsForPages([]string{page.ID})
	if err != nil || len(tags[page.ID]) != 1 || tags[page.ID][0] != "new" {
		t.Fatalf("stale tags: %v %v", tags, err)
	}
	props, err := w.props.GetPropertiesForPages([]string{page.ID})
	if err != nil || props[page.ID]["project"].Value != "new" {
		t.Fatalf("stale properties: %v %v", props, err)
	}
	back, err := w.links.GetBacklinksForPage(target.ID)
	if err != nil || len(back.Backlinks) != 0 {
		t.Fatalf("stale links: %+v %v", back, err)
	}
	// Restore uses the existing usecase and triggers all derived effects.
	if _, err := wikirevisions.NewRestoreRevisionUseCase(w.revision, w.tree, w.newPageOrchestrator(), w.log, nil).Execute(context.Background(), wikirevisions.RestoreRevisionInput{PageID: page.ID, RevisionID: revisionID, UserID: "system"}); err != nil {
		t.Fatal(err)
	}
	assertDerivedTerm(t, w, page.ID, "以前", 1)
	filtered, err := wikisearch.NewSearchUseCase(w.searchIndex, w.tags, w.tree).Execute(context.Background(), wikisearch.SearchInput{Query: "以前", Tags: []string{"old"}, Limit: 10})
	if err != nil || filtered.Result.Count != 1 || len(filtered.Result.TagFacets) != 1 || filtered.Result.TagFacets[0].Tag != "old" {
		t.Fatalf("search filters/facets: %+v %v", filtered, err)
	}
	tagOnly, err := wikisearch.NewSearchUseCase(w.searchIndex, w.tags, w.tree).Execute(context.Background(), wikisearch.SearchInput{Tags: []string{"old"}, Limit: 1})
	if err != nil || tagOnly.Result.Count != 1 || len(tagOnly.Result.Items) != 1 {
		t.Fatalf("tag-only search: %+v %v", tagOnly, err)
	}
	back, err = w.links.GetBacklinksForPage(target.ID)
	if err != nil || len(back.Backlinks) != 1 {
		t.Fatalf("restored backlinks: %+v %v", back, err)
	}
	// Renaming a parent also updates descendant search paths.
	parent := createPageForTest(t, w, "system", nil, "Folder", "folder", pageNodeKind())
	current, err = w.tree.GetPage(page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := wikipages.NewMovePageUseCase(w.tree, w.newPageOrchestrator(), w.log, nil).Execute(context.Background(), wikipages.MovePageInput{UserID: "system", ID: page.ID, Version: current.Version(), ParentID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	updatePageForTest(t, w, "system", parent.ID, parent.Title, "renamed", nil, nil)
	result, err := w.searchIndex.Search("以前", []string{page.ID}, 0, 10)
	if err != nil || len(result.Items) != 1 || result.Items[0].Path != "renamed/source" {
		t.Fatalf("descendant path: %+v %v", result, err)
	}
	deletePageForTest(t, w, "system", page.ID, false)
	derivedEffects(w).Run(pagesave.PageSaveEvent{Operation: pagesave.PageOperationRestore, After: old})
	assertDerivedTerm(t, w, page.ID, "以前", 0)
	back, err = w.links.GetBacklinksForPage(target.ID)
	if err != nil || len(back.Backlinks) != 0 {
		t.Fatalf("deleted source resurrected: %+v %v", back, err)
	}
}

func TestPostgresDerivedFailureAndRecovery(t *testing.T) {
	w := newDerivedWiki(t)
	page := createPageForTest(t, w, "system", nil, "Failure", "failure", pageNodeKind())
	ctx := context.Background()
	// Force only the search projection transaction to fail after canonical commit.
	_, err := w.pg.DB().Exec(ctx, `CREATE FUNCTION fail_search_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected index failure'; END $$;
 CREATE TRIGGER fail_search_write BEFORE INSERT OR UPDATE ON search_pages FOR EACH ROW EXECUTE FUNCTION fail_search_write();`)
	if err != nil {
		t.Fatal(err)
	}
	updated := updateDerivedPage(t, w, page, "正本保持の検証")
	persisted, err := w.tree.GetPage(page.ID)
	if err != nil || persisted.Content != updated.Content {
		t.Fatalf("canonical rollback by derived error: %+v %v", persisted, err)
	}
	if _, err := w.pg.DB().Exec(ctx, `DROP TRIGGER fail_search_write ON search_pages; DROP FUNCTION fail_search_write()`); err != nil {
		t.Fatal(err)
	}
	if err := w.ReloadFromFS(); err != nil {
		t.Fatal(err)
	}
	assertDerivedTerm(t, w, page.ID, "正本保持", 1)
	// Terminate pooled connections and use the same services afterwards.
	if _, err := w.pg.DB().Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	// A lost idle connection may fail its first operation; pool health replaces it.
	_ = w.searchIndex.Ping()
	if err := w.searchIndex.Ping(); err != nil {
		t.Fatal(err)
	}
	updateDerivedPage(t, w, page, "再接続後の検索")
	assertDerivedTerm(t, w, page.ID, "再接続", 1)
}

func TestPostgresConcurrentDerivedRebuild(t *testing.T) {
	w := newDerivedWiki(t)
	page := createPageForTest(t, w, "system", nil, "Concurrent", "concurrent", pageNodeKind())
	target := createPageForTest(t, w, "system", nil, "Destination", "destination", pageNodeKind())
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- w.ReloadFromFS() }()
	go func() { defer wg.Done(); errs <- w.links.IndexAllPages() }()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 6; i++ {
			current, err := w.tree.GetPage(page.ID)
			if err != nil {
				errs <- err
				return
			}
			raw := fmt.Sprintf("---\ntags: [tag%d]\nproject: value%d\n---\nConcurrent corpus%d [[Destination]]", i, i, i)
			_, err = wikipages.NewUpdatePageUseCase(w.tree, w.slug, w.newPageOrchestrator(), w.log, nil).Execute(context.Background(), wikipages.UpdatePageInput{UserID: "system", ID: page.ID, Title: page.Title, Slug: page.Slug, Version: current.Version(), Content: &raw, PreserveFrontmatter: true})
			if err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertDerivedTerm(t, w, page.ID, "corpus5", 1)
	back, err := w.links.GetBacklinksForPage(target.ID)
	if err != nil || len(back.Backlinks) != 1 {
		t.Fatalf("concurrent backlinks: %+v %v", back, err)
	}
	tags, err := w.tags.GetTagsForPages([]string{page.ID})
	if err != nil || strings.Join(tags[page.ID], ",") != "tag5" {
		t.Fatalf("concurrent tags: %v %v", tags, err)
	}
	props, err := w.props.GetPropertiesForPages([]string{page.ID})
	if err != nil || props[page.ID]["project"].Value != "value5" {
		t.Fatalf("concurrent properties: %v %v", props, err)
	}
}
