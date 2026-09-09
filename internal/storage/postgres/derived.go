package postgres

import (
	"database/sql"
	"errors"
	"strings"
)

// CurrentPageMatches locks only the source page until the derived write commits.
// An old post-commit callback must not replace an index built from a newer page,
// nor resurrect an index after deletion. Path is checked for hierarchy-sensitive
// projections because moving an ancestor need not change the child's Markdown.
func CurrentPageMatches(tx *sql.Tx, id, raw string, path *string) (bool, error) {
	var current string
	err := tx.QueryRow(`SELECT coalesce(content_markdown,'') FROM pages WHERE id=$1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || current != raw {
		return false, err
	}
	if path != nil {
		var currentPath string
		err = tx.QueryRow(`WITH RECURSIVE ancestors AS (
   SELECT id,parent_id,slug,0 AS depth FROM pages WHERE id=$1
   UNION ALL SELECT p.id,p.parent_id,p.slug,a.depth+1 FROM pages p JOIN ancestors a ON a.parent_id=p.id
  ) SELECT coalesce(string_agg(slug,'/' ORDER BY depth DESC) FILTER (WHERE id<>'root'),'') FROM ancestors`, id).Scan(&currentPath)
		if err != nil || currentPath != strings.Trim(*path, "/") {
			return false, err
		}
	}
	return true, nil
}

// ASCIIFold is used only with repository-owned SQL expressions. It preserves
// SQLite NOCASE/LIKE's ASCII-only case folding rather than changing Unicode
// tag/property/path matching as an incidental effect of the database migration.
func ASCIIFold(expression string) string {
	return "translate(" + expression + ",'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz')"
}
