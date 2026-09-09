package search

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/perber/wiki/internal/core/excerpt"
	"github.com/perber/wiki/internal/core/markdown"
	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/storage/postgres"
)

type PostgreSQLIndex struct{ db *sql.DB }

func NewPostgreSQLIndex(store *postgres.Store) (*PostgreSQLIndex, error) {
	if store == nil {
		return nil, fmt.Errorf("PostgreSQL store is required")
	}
	return &PostgreSQLIndex{db: store.SQLDB()}, nil
}
func (s *PostgreSQLIndex) Clear() error { _, err := s.db.Exec(`DELETE FROM search_pages`); return err }
func (s *PostgreSQLIndex) Ping() error  { return s.db.Ping() }
func (s *PostgreSQLIndex) Close() error { return s.db.Close() }
func (s *PostgreSQLIndex) IndexPages(inputs []IndexPageInput) ([]IndexFailure, error) {
	inputs = append([]IndexPageInput(nil), inputs...)
	sort.SliceStable(inputs, func(i, j int) bool { return inputs[i].PageID < inputs[j].PageID })
	var failures []IndexFailure
	err := postgres.WithSQLTx(s.db, func(tx *sql.Tx) error {
		for _, in := range inputs {
			if in.CurrentPage {
				matches, err := postgres.CurrentPageMatches(tx, in.PageID, in.Raw, &in.Path)
				if err != nil {
					return err
				}
				if !matches {
					continue
				}
			}
			_, body, _, err := markdown.ParseFrontmatter(in.Raw)
			if err != nil {
				failures = append(failures, IndexFailure{PageID: in.PageID, Err: err})
				continue
			}
			body = excerpt.NormalizeMarkdownBody(body)
			_, err = tx.Exec(`INSERT INTO search_pages(page_id,path,filepath,kind,title,headings,content)
    VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(page_id) DO UPDATE SET
    path=excluded.path,filepath=excluded.filepath,kind=excluded.kind,title=excluded.title,headings=excluded.headings,content=excluded.content`,
				in.PageID, in.Path, in.FilePath, string(in.Kind), in.Title, extractHeadings(body), excerpt.PlainTextForSearch(body))
			if err != nil {
				return err
			}
		}
		return nil
	})
	return failures, err
}
func (s *PostgreSQLIndex) IndexPage(path, filePath, pageID, title string, kind tree.NodeKind, raw string) error {
	failures, err := s.IndexPages([]IndexPageInput{{Path: path, FilePath: filePath, PageID: pageID, Title: title, Kind: kind, Raw: raw}})
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		return failures[0].Err
	}
	return nil
}
func (s *PostgreSQLIndex) RemovePages(ids []string) error {
	return postgres.WithSQLTx(s.db, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(`DELETE FROM search_pages WHERE page_id=$1`, id); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *PostgreSQLIndex) RemovePage(id string) error { return s.RemovePages([]string{id}) }
func (s *PostgreSQLIndex) RemovePageByFilePath(path string) (int64, error) {
	result, err := s.db.Exec(`DELETE FROM search_pages WHERE filepath=$1`, path)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func postgresSearchWhere(query string, ids []string) (string, []any, error) {
	where := "TRUE"
	var args []any
	if query != "" {
		var err error
		where, args, err = compilePostgresQuery(query)
		if err != nil {
			return "", nil, err
		}
	}

	if ids != nil {
		where += " AND page_id IN (" + postgres.Placeholders(len(args)+1, len(ids)) + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	return where, args, nil
}
func (s *PostgreSQLIndex) Search(query string, ids []string, offset, limit int) (*SearchResult, error) {
	query = strings.TrimSpace(query)
	result := &SearchResult{Offset: offset, Limit: limit, TagFacets: []SearchTagFacet{}}
	if (ids != nil && len(ids) == 0) || (query == "" && len(ids) == 0) {
		result.Items = []SearchResultItem{}
		return result, nil
	}
	where, args, err := postgresSearchWhere(query, ids)
	if err != nil {
		return nil, err
	}
	err = postgres.WithSQLTx(s.db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY`); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT count(*) FROM search_pages WHERE `+where, args...).Scan(&result.Count); err != nil {
			return err
		}
		order := postgres.ASCIIFold("title") + "," + postgres.ASCIIFold("path") + ",page_id"
		if query != "" {
			order = "pgroonga_score(tableoid,ctid) DESC,page_id"
		}
		titleExpr, snippetExpr := "title", "''::text"
		if query != "" {
			titleExpr = `pgroonga_highlight_html(title,pgroonga_query_extract_keywords($1))`
			snippetExpr = `coalesce((pgroonga_snippet_html(content,pgroonga_query_extract_keywords($1)))[1],'')`
		}
		q := `SELECT page_id,path,kind,` + titleExpr + `,content,` + snippetExpr + ` FROM search_pages WHERE ` + where + ` ORDER BY ` + order + fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		rows, err := tx.Query(q, append(args, limit, offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SearchResultItem
			var body string
			if err := rows.Scan(&item.PageID, &item.Path, &item.Kind, &item.Title, &body, &item.Excerpt); err != nil {
				return err
			}
			if query == "" {
				item.Title = sanitizeSearchTitle(item.Title)
			} else {
				item.Title = postgresHighlight(item.Title)
			}
			if item.Excerpt == "" {
				item.Excerpt = excerpt.FromBody(body)
			} else {
				item.Excerpt = postgresHighlight(item.Excerpt)
			}
			item.Rank = 1
			result.Items = append(result.Items, item)
		}
		return rows.Err()
	})
	return result, err
}
func (s *PostgreSQLIndex) SearchPageIDs(query string, ids []string) ([]string, error) {
	query = strings.TrimSpace(query)
	if (ids != nil && len(ids) == 0) || (query == "" && len(ids) == 0) {
		return []string{}, nil
	}
	where, args, err := postgresSearchWhere(query, ids)
	if err != nil {
		return nil, err
	}
	order := postgres.ASCIIFold("title") + "," + postgres.ASCIIFold("path") + ",page_id"
	if query != "" {
		order = "pgroonga_score(tableoid,ctid) DESC,page_id"
	}
	rows, err := s.db.Query(`SELECT page_id FROM search_pages WHERE `+where+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// PGroonga escapes source HTML before inserting its own markers.
func postgresHighlight(s string) string {
	return strings.NewReplacer(`<span class="keyword">`, "<b>", "</span>", "</b>").Replace(s)
}
