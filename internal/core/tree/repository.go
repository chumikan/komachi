package tree

import (
	"context"
	"github.com/perber/wiki/internal/core/ignore"
)

// nodeRepository is the persistence boundary used by the existing service.
type nodeRepository interface {
	SetIgnoreCache(ignoreCache *ignore.Cache)
	LoadTree(filename string) (*PageNode, error)
	ReconstructTreeFromFS() (*PageNode, error)
	ReconstructTreeFromFSContext(ctx context.Context) (*PageNode, error)
	SaveChildOrder(parent *PageNode) error
	CreatePage(parentEntry *PageNode, newEntry *PageNode) error
	CreateSection(parentEntry *PageNode, newEntry *PageNode) error
	UpsertContent(entry *PageNode, content string) error
	UpsertContentPreservingFrontmatter(entry *PageNode, content string) error
	UpsertContentAndMetadata(
		entry *PageNode,
		body string,
		tags []string,
		properties map[string]string,
	) error
	MoveNode(entry *PageNode, parentEntry *PageNode) error
	DeletePage(entry *PageNode) error
	DeleteSection(entry *PageNode) error
	RenameNode(entry *PageNode, newSlug string) error
	ReadPageRaw(entry *PageNode) (string, error)
	ReadPageAndRaw(entry *PageNode) (content, raw string, err error)
	ReadPageContent(entry *PageNode) (string, error)
	SyncFrontmatterIfExists(entry *PageNode) error
	dirPathForNode(entry *PageNode) (string, error)
	ConvertNode(entry *PageNode, target NodeKind) error
	SetPinnedFrontmatter(entry *PageNode, pinned bool) (string, error)
}
