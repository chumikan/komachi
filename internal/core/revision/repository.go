package revision

import (
	"io"
	"os"
)

// revisionRepository is the persistence boundary used by the existing service.
type revisionRepository interface {
	SaveContentBlob(pageID string, content []byte) (string, error)
	SaveAssetBlobFromPath(srcPath string) (string, int64, error)
	SaveAssetManifest(items []AssetRef) (string, error)
	SaveRevision(rev *Revision) error
	ListRevisions(pageID string) ([]*Revision, error)
	ListRevisionsPage(pageID, cursor string, limit int) ([]*Revision, string, error)
	GetLatestRevision(pageID string) (*Revision, error)
	GetRevision(pageID, revisionID string) (*Revision, error)
	UpdateRevision(rev *Revision) error
	PruneRevisions(pageID string, keepCount int) error
	ReadContentBlob(pageID, hash string) ([]byte, error)
	OpenContentBlob(pageID, hash string) (io.ReadCloser, error)
	DeleteContentBlobIfUnreferenced(pageID, hash string) error
	LoadAssetManifest(hash string) ([]AssetRef, error)
	OpenAssetBlob(hash string) (*os.File, error)
	CopyAssetBlobToPath(hash string, expectedSize int64, dstPath string) error
	DeletePageRevisions(pageID string) error
	contentBlobPath(pageID, hash string) string
	AssetBlobPath(hash string) string
	AssetManifestExists(hash string) bool
	assetManifestPath(hash string) string
}
