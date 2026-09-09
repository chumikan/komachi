package transfer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/perber/wiki/internal/core/revision"
)

func revisionFixture(t *testing.T) (string, *revision.Revision) {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "schema.json", `{"version":5}`)
	writeFixture(t, root, "root/page.md", pageFixture("page-id", "ページ"))
	store := revision.NewFSStore(root, nil)
	contentHash, err := store.SaveContentBlob("page-id", []byte("# 過去の本文\n"))
	if err != nil {
		t.Fatal(err)
	}
	manifestHash, err := store.SaveAssetManifest(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &revision.Revision{ID: "original-revision-id", PageID: "page-id", AuthorID: "system", CreatorID: "system", LastAuthorID: "system", CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 123456789, time.UTC), Title: "旧タイトル", Slug: "old-slug", Kind: "page", Path: "old-parent/old-slug", ParentID: "old-parent", ContentHash: contentHash, AssetManifestHash: manifestHash, Type: revision.RevisionTypeContentUpdate, ExtraFrontmatter: map[string]interface{}{"custom": "旧値"}}
	if err := store.SaveRevision(r); err != nil {
		t.Fatal(err)
	}
	return root, r
}

func TestInspectionPreservesRevisionIdentityAndSource(t *testing.T) {
	root, want := revisionFixture(t)
	ctx := context.Background()
	before, err := Inventory(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := InspectLegacy(ctx, root)
	if err != nil || !r.Valid() {
		t.Fatalf("inspection: %v %+v", err, r)
	}
	if r.Committed || r.Counts["revisions"] != 1 || r.Counts["pages"] != 1 {
		t.Fatalf("report: %+v", r)
	}
	pages, err := inspectPages(ctx, root, &r)
	if err != nil {
		t.Fatal(err)
	}
	history, err := inspectRevisions(ctx, root, before, pages, &r)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatal("history missing")
	}
	gotRaw, _ := json.Marshal(history[0].Revision)
	wantRaw, _ := json.Marshal(want)
	if string(gotRaw) != string(wantRaw) || history[0].Content != "# 過去の本文\n" {
		t.Fatal("revision metadata/content changed")
	}
	after, err := Inventory(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if inventoryHash(before) != inventoryHash(after) {
		t.Fatal("inspection rewrote revision/index/blob")
	}
}

func TestInspectionRejectsMissingRevisionBlob(t *testing.T) {
	root, rev := revisionFixture(t)
	path := filepath.Join(root, ".leafwiki", "blobs", "content", rev.PageID, "sha256", rev.ContentHash[:2], rev.ContentHash)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r, err := InspectLegacy(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if r.Valid() {
		t.Fatal("accepted missing revision content")
	}
	for _, issue := range r.Issues {
		if issue.Code == "missing_revision_blob" {
			return
		}
	}
	t.Fatalf("report: %+v", r)
}

func TestInspectionSupportsLegacyGlobalContentBlob(t *testing.T) {
	root, rev := revisionFixture(t)
	scoped := filepath.Join(root, ".leafwiki", "blobs", "content", rev.PageID, "sha256", rev.ContentHash[:2], rev.ContentHash)
	global := filepath.Join(root, ".leafwiki", "blobs", "content", "sha256", rev.ContentHash[:2], rev.ContentHash)
	if err := os.MkdirAll(filepath.Dir(global), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(scoped, global); err != nil {
		t.Fatal(err)
	}
	r, err := InspectLegacy(context.Background(), root)
	if err != nil || !r.Valid() || r.Counts["revisions"] != 1 {
		t.Fatalf("legacy fallback: %v %+v", err, r)
	}
}

func TestInspectionEmptyWorkspaceAndUnsupportedSchema(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	r, err := InspectLegacy(ctx, root)
	if err != nil || !r.Valid() || r.Committed || r.Counts["pages"] != 0 {
		t.Fatalf("empty: %v %+v", err, r)
	}
	writeFixture(t, root, "root/page.md", pageFixture("page-id", "ページ"))
	writeFixture(t, root, "schema.json", `{"version":4}`)
	r, err = InspectLegacy(ctx, root)
	if err != nil || r.Valid() {
		t.Fatalf("unsupported schema: %v %+v", err, r)
	}
}
