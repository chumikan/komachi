package transfer

import (
	"context"
	"strings"

	"github.com/perber/wiki/internal/core/tree"
	"github.com/perber/wiki/internal/links"
	"github.com/perber/wiki/internal/properties"
	"github.com/perber/wiki/internal/search"
	"github.com/perber/wiki/internal/storage/postgres"
	"github.com/perber/wiki/internal/tags"
)

// RebuildDerived uses the same extraction/resolution repositories as runtime,
// without NewWiki's default-user/welcome-page bootstrap. Run with writers stopped.
func RebuildDerived(ctx context.Context, pg *postgres.Store, dataDir string) error {
	t := tree.NewPostgresTreeService(dataDir, pg)
	if err := t.LoadTree(); err != nil {
		return err
	}
	ls, err := links.NewPostgresLinksStore(pg)
	if err != nil {
		return err
	}
	defer ls.Close()
	ts, err := tags.NewPostgresTagsStore(pg)
	if err != nil {
		return err
	}
	defer ts.Close()
	ps, err := properties.NewPostgresPropertiesStore(pg)
	if err != nil {
		return err
	}
	defer ps.Close()
	index, err := search.NewPostgreSQLIndex(pg)
	if err != nil {
		return err
	}
	defer index.Close()
	if err := links.NewLinkService(dataDir, t, ls).IndexAllPagesContext(ctx); err != nil {
		return err
	}
	tagService := tags.NewTagsService(ts)
	propService := properties.NewPropertiesService(ps)
	if err := tagService.ClearIndex(); err != nil {
		return err
	}
	if err := propService.ClearIndex(); err != nil {
		return err
	}
	if err := index.Clear(); err != nil {
		return err
	}
	return t.WalkNodes(func(id string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := t.GetPage(id)
		if err != nil {
			return err
		}
		if err := tagService.IndexPageContent(id, p.RawContent); err != nil {
			return err
		}
		if err := propService.IndexPageContent(id, p.RawContent); err != nil {
			return err
		}
		path := strings.TrimPrefix(p.CalculatePath(), "/")
		filePath := path
		if filePath != "" {
			filePath += ".md"
		}
		return index.IndexPage(path, filePath, id, p.Title, p.Kind, p.RawContent)
	})
}
