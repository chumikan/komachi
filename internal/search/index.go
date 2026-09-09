package search

import "github.com/perber/wiki/internal/core/tree"

// Index contains only the existing application search operations.
type Index interface {
	Clear() error
	Ping() error
	Close() error
	IndexPages([]IndexPageInput) ([]IndexFailure, error)
	IndexPage(string, string, string, string, tree.NodeKind, string) error
	RemovePages([]string) error
	RemovePage(string) error
	RemovePageByFilePath(string) (int64, error)
	Search(string, []string, int, int) (*SearchResult, error)
	SearchPageIDs(string, []string) ([]string, error)
}
