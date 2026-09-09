package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/perber/wiki/internal/core/tree"
)

func writeFixture(t *testing.T, root, path, raw string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}
func pageFixture(id, title string) string {
	return fmt.Sprintf("---\nleafwiki_id: %s\nleafwiki_title: %s\nleafwiki_created_at: '2025-01-01T00:00:00.123456789Z'\nleafwiki_updated_at: '2025-01-02T00:00:00.987654321Z'\nleafwiki_creator_id: system\nleafwiki_last_author_id: system\nleafwiki_pinned: true\ncustom:\n  nested: [日本語, example]\ntags: [テスト]\n---\n# 本文\n[[別ページ]]\n", id, title)
}

func TestPageInspectionMatchesLegacyTreeWithoutWriteback(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFixture(t, root, "schema.json", `{"version":5}`)
	for path, raw := range map[string]string{
		"root/B.md":           pageFixture("b", "重複タイトル"),
		"root/a.md":           pageFixture("a", "重複タイトル"),
		"root/group/index.md": pageFixture("section", "見出し"),
		"root/group/a.md":     pageFixture("nested", "重複タイトル"),
		"root/.order.json":    `{"ordered_ids":["section","b"]}`,
	} {
		writeFixture(t, root, path, raw)
	}
	before, err := Inventory(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	report := Report{Counts: map[string]int{}}
	pages, err := inspectPages(ctx, root, &report)
	if err != nil || !report.Valid() {
		t.Fatalf("inspection: %v %+v", err, report)
	}
	if report.Counts["pages"] != 3 || report.Counts["sections"] != 1 {
		t.Fatalf("counts: %+v", report.Counts)
	}
	legacy, err := tree.NewNodeStore(root).ReconstructTreeFromFS()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pages[0].Node, legacy) {
		t.Fatal("read-only tree differs from legacy reconstruction")
	}
	for _, p := range pages[1:] {
		path := filepath.Join(root, p.Source)
		if p.Node.Kind == tree.NodeKindSection {
			path = filepath.Join(path, "index.md")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if p.Raw == nil || *p.Raw != string(raw) {
			t.Fatal("raw Markdown changed")
		}
	}
	after, err := Inventory(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if inventoryHash(before) != inventoryHash(after) {
		t.Fatal("inspection changed source")
	}
}

func TestPageInspectionReportsUnrepresentableData(t *testing.T) {
	for _, tc := range []struct{ name, path, raw, code string }{
		{"missing ID", "root/a.md", "# 本文\n", "missing_page_id"},
		{"invalid YAML", "root/a.md", "---\nleafwiki_id: [\n---\n", "malformed_frontmatter"},
		{"invalid UTF8", "root/a.md", string([]byte{255}), "unsupported_text"},
		{"NUL", "root/a.md", "a\x00b", "unsupported_text"},
		{"root index", "root/index.md", pageFixture("ignored", "Ignored"), "unrepresented_index"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "schema.json", `{"version":5}`)
			writeFixture(t, root, tc.path, tc.raw)
			r := Report{Counts: map[string]int{}}
			_, err := inspectPages(context.Background(), root, &r)
			if err != nil {
				t.Fatal(err)
			}
			for _, issue := range r.Issues {
				if issue.Code == tc.code {
					return
				}
			}
			t.Fatalf("missing %s: %+v", tc.code, r)
		})
	}
}
