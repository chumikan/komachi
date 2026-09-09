package tags

import (
	"context"
	"github.com/perber/wiki/internal/test_utils"
	"testing"
)

func TestPostgresStorageContract(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var store *TagsStore
			var err error
			if backend == "sqlite" {
				store, err = NewTagsStore(t.TempDir())
			} else {
				db, _ := test_utils.PostgresStore(t)
				if err = db.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				store, err = NewPostgresTagsStore(db)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			svc := NewTagsService(store)
			indexContent := func(id, raw string) error {
				if backend == "postgres" {
					if _, err := store.db.Exec(`INSERT INTO pages(id,parent_id,title,slug,kind,position,metadata,content_markdown) VALUES ($1,'root',$1,$1,'page',0,'{}',$2) ON CONFLICT(id) DO UPDATE SET content_markdown=excluded.content_markdown`, id, raw); err != nil {
						return err
					}
				}
				return svc.IndexPageContent(id, raw)
			}
			if err := indexContent("one", "---\ntags: [Go, 東京, shared]\n---\nBody"); err != nil {
				t.Fatal(err)
			}
			if err := indexContent("two", "---\ntags: [Go, shared]\n---\nOther"); err != nil {
				t.Fatal(err)
			}
			prefix, err := svc.GetAllTags("GO", 10)
			if err != nil || len(prefix) != 1 {
				t.Fatalf("prefix case: %v %v", prefix, err)
			}
			ids, err := svc.GetPageIDsByTags([]string{"go", "東京"})
			if err != nil || len(ids) != 1 || ids[0] != "one" {
				t.Fatalf("AND: %v %v", ids, err)
			}
			counts, err := svc.GetAllTagsForSelection("", []string{"go"}, 10)
			if err != nil || len(counts) != 2 {
				t.Fatalf("suggestions: %+v %v", counts, err)
			}
			if err := indexContent("one", "---\ntags: [new]\n---\nUpdated"); err != nil {
				t.Fatal(err)
			}
			ids, err = svc.GetPageIDsByTags([]string{"東京"})
			if err != nil || len(ids) != 0 {
				t.Fatalf("update: %v %v", ids, err)
			}
			if err := store.DeletePageIndexes([]string{"one", "two"}); err != nil {
				t.Fatal(err)
			}
			counts, err = svc.GetAllTags("", 10)
			if err != nil || len(counts) != 0 {
				t.Fatalf("delete: %v %v", counts, err)
			}
		})
	}
}
