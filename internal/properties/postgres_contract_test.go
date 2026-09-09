package properties

import (
	"context"
	"github.com/perber/wiki/internal/test_utils"
	"testing"
)

func TestPostgresStorageContract(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var store *PropertiesStore
			var err error
			if backend == "sqlite" {
				store, err = NewPropertiesStore(t.TempDir())
			} else {
				db, _ := test_utils.PostgresStore(t)
				if err = db.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				store, err = NewPostgresPropertiesStore(db)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			svc := NewPropertiesService(store)
			indexContent := func(id, raw string) error {
				if backend == "postgres" {
					if _, err := store.db.Exec(`INSERT INTO pages(id,parent_id,title,slug,kind,position,metadata,content_markdown) VALUES ($1,'root',$1,$1,'page',0,'{}',$2) ON CONFLICT(id) DO UPDATE SET content_markdown=excluded.content_markdown`, id, raw); err != nil {
						return err
					}
				}
				return svc.IndexPageContent(id, raw)
			}
			if err := indexContent("one", "---\nauthor: 東京\nproject: Go\ntags: [ignored]\n---\nBody"); err != nil {
				t.Fatal(err)
			}
			prefix, err := svc.GetAllPropertyKeys("PRO", 10)
			if err != nil || len(prefix) != 1 {
				t.Fatalf("prefix case: %v %v", prefix, err)
			}
			ids, err := svc.GetPageIDsByProperty("author", "東京")
			if err != nil || len(ids) != 1 {
				t.Fatalf("exact: %v %v", ids, err)
			}
			ids, err = svc.GetPageIDsByProperty("project", "go")
			if err != nil || len(ids) != 0 {
				t.Fatalf("case: %v %v", ids, err)
			}
			props, err := svc.GetPropertiesForPages([]string{"one"})
			if err != nil || len(props["one"]) != 2 {
				t.Fatalf("extraction: %v %v", props, err)
			}
			if err := indexContent("one", "---\nproject: New\n---\nUpdated"); err != nil {
				t.Fatal(err)
			}
			ids, err = svc.GetPageIDsByProperty("author", "東京")
			if err != nil || len(ids) != 0 {
				t.Fatalf("update: %v %v", ids, err)
			}
			if err := store.DeletePropertiesForPage("one"); err != nil {
				t.Fatal(err)
			}
			keys, err := svc.GetAllPropertyKeys("", 10)
			if err != nil || len(keys) != 0 {
				t.Fatalf("delete: %v %v", keys, err)
			}
		})
	}
}
