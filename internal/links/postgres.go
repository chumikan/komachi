package links

import (
	"database/sql"
	"strings"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/storage/postgres"
)

// Healing is guarded too: an old callback must not resolve a link to a page
// which has since been deleted or moved.
func (s *LinksStore) healCurrentPage(page *tree.Page, wikiTitle bool) error {
	path := normalizeWikiPath(page.CalculatePath())
	return postgres.WithSQLTx(s.db, func(tx *sql.Tx) error {
		matches, err := postgres.CurrentPageMatches(tx, page.ID, page.RawContent, &path)
		if err != nil || !matches {
			return err
		}
		if wikiTitle {
			_, err = tx.Exec(`UPDATE links SET to_page_id=$1,broken=0 WHERE `+s.foldedPath()+`=$2 AND broken=1`, page.ID, strings.ToLower(wikilinkSentinelPrefix+page.Title))
		} else {
			_, err = tx.Exec(`UPDATE links SET to_page_id=$1,broken=0 WHERE to_path=$2 AND broken=1`, page.ID, path)
		}
		return err
	})
}
