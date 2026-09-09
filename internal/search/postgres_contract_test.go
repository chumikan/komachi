package search

import (
	"context"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/test_utils"
	"testing"
)

func TestSearchStorageContract(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var index Index
			var err error
			if backend == "sqlite" {
				index, err = NewSQLiteIndex(t.TempDir())
			} else {
				store, _ := test_utils.PostgresStore(t)
				if err = store.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				index, err = NewPostgreSQLIndex(store)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer index.Close()
			for _, in := range []IndexPageInput{
				{PageID: "title", Path: "title", Title: "Tokyo guide", Kind: tree.NodeKindPage, Raw: "Japanese capital"},
				{PageID: "body", Path: "body", Title: "Travel", Kind: tree.NodeKindPage, Raw: "Visit Tokyo today"},
				{PageID: "other", Path: "other", Title: "Kyoto", Kind: tree.NodeKindPage, Raw: "Old capital"},
			} {
				if err := index.IndexPage(in.Path, in.Path+".md", in.PageID, in.Title, in.Kind, in.Raw); err != nil {
					t.Fatal(err)
				}
			}

			queries := map[string]int{`Tokyo AND guide`: 1, `Tokyo OR Kyoto`: 3, `"Tokyo guide"`: 1, `Tok*`: 2, `title:Tokyo`: 1, `content:Tokyo`: 1, `NEAR(Tokyo guide)`: 1, `(Tokyo OR Kyoto) AND capital`: 2, `{title content}:Tokyo`: 2, `- title:Tokyo`: 1, `path:title`: 0, `Tokyo NOT "guide"`: 1, `^Tokyo`: 1}
			for q, want := range queries {
				found, err := index.Search(q, nil, 0, 10)
				if err != nil || found.Count != want {
					t.Fatalf("query %q: %+v %v, want %d", q, found, err, want)
				}
			}

			result, err := index.Search("Tokyo", nil, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if result.Count != 2 || len(result.Items) != 1 || result.Items[0].PageID != "title" {
				t.Fatalf("ranking/pagination: %+v", result)
			}
			result, err = index.Search("Tokyo", []string{"body"}, 0, 10)
			if err != nil || result.Count != 1 {
				t.Fatalf("filter: %+v %v", result, err)
			}
			result, err = index.Search("", nil, 0, 10)
			if err != nil || result.Count != 0 || result.Items == nil {
				t.Fatalf("empty: %+v %v", result, err)
			}
			if err := index.RemovePage("title"); err != nil {
				t.Fatal(err)
			}
			result, err = index.Search("Tokyo", nil, 0, 10)
			if err != nil || result.Count != 1 {
				t.Fatalf("delete: %+v %v", result, err)
			}
		})
	}
}

func TestPostgresJapaneseSearch(t *testing.T) {
	store, _ := test_utils.PostgresStore(t)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	index, err := NewPostgreSQLIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	for _, in := range []IndexPageInput{
		{PageID: "title", Title: "東京都の案内", Raw: "首都の情報です。"},
		{PageID: "body", Title: "旅行", Raw: "**東京都**をGo言語で紹介します。"},
		{PageID: "other", Title: "京都", Raw: "古都の案内です。"},
	} {
		if err := index.IndexPage(in.PageID, in.PageID+".md", in.PageID, in.Title, tree.NodeKindPage, in.Raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"東京", "東京都"} {
		result, err := index.Search(q, nil, 0, 10)
		if err != nil || result.Count != 2 || result.Items[0].PageID != "title" {
			t.Fatalf("%s: %+v %v", q, result, err)
		}
	}
	result, err := index.Search("東京 Go", nil, 0, 10)
	if err != nil || result.Count != 1 || result.Items[0].PageID != "body" {
		t.Fatalf("mixed: %+v %v", result, err)
	}
	if _, err := store.DB().Exec(context.Background(), `REINDEX INDEX search_pages_full_text`); err != nil {
		t.Fatal(err)
	}
	result, err = index.Search("東京", nil, 0, 10)
	if err != nil || result.Count != 2 {
		t.Fatalf("reindex: %+v %v", result, err)
	}
}

func TestPostgresSearchBatchRollback(t *testing.T) {
	store, _ := test_utils.PostgresStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	index, err := NewPostgreSQLIndex(store)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if _, err := store.DB().Exec(ctx, `ALTER TABLE search_pages ADD CHECK (title <> 'reject')`); err != nil {
		t.Fatal(err)
	}
	_, err = index.IndexPages([]IndexPageInput{{PageID: "a", Title: "accepted", Raw: "first"}, {PageID: "b", Title: "reject", Raw: "second"}})
	if err == nil {
		t.Fatal("expected batch failure")
	}
	var count int
	if err := store.DB().QueryRow(ctx, `SELECT count(*) FROM search_pages`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial batch commit: %d %v", count, err)
	}
}
