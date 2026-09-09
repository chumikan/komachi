package revision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/perber/wiki/internal/storage/postgres"
)

// Only attachment bytes remain in FSStore. Revision metadata, content and
// manifests never fall back to disk when using this repository.
type postgresStore struct {
	*FSStore
	db  postgres.DBTX
	ctx context.Context
}

func (s *postgresStore) SaveContentBlob(pageID string, content []byte) (string, error) {
	hash := sha256HexBytes(content)
	_, e := s.db.Exec(s.ctx, `INSERT INTO revision_contents(page_id,content_hash,content_markdown) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, pageID, hash, string(content))
	return hash, e
}
func (s *postgresStore) ReadContentBlob(pageID, hash string) ([]byte, error) {
	if hash == "" {
		return []byte{}, nil
	}
	var body string
	e := s.db.QueryRow(s.ctx, `SELECT content_markdown FROM revision_contents WHERE page_id=$1 AND content_hash=$2`, pageID, hash).Scan(&body)
	if errors.Is(e, pgx.ErrNoRows) {
		e = os.ErrNotExist
	}
	return []byte(body), e
}
func (s *postgresStore) OpenContentBlob(pageID, hash string) (io.ReadCloser, error) {
	b, e := s.ReadContentBlob(pageID, hash)
	if e != nil {
		return nil, e
	}
	return io.NopCloser(strings.NewReader(string(b))), nil
}
func (s *postgresStore) SaveAssetManifest(items []AssetRef) (string, error) {
	raw, e := json.Marshal(assetManifest{Items: cloneAndSortAssetRefs(items)})
	if e != nil {
		return "", e
	}
	hash := sha256HexBytes(raw)
	_, e = s.db.Exec(s.ctx, `INSERT INTO revision_asset_manifests(hash,manifest) VALUES($1,$2) ON CONFLICT DO NOTHING`, hash, raw)
	return hash, e
}
func (s *postgresStore) LoadAssetManifest(hash string) ([]AssetRef, error) {
	if hash == "" {
		return []AssetRef{}, nil
	}
	var raw []byte
	e := s.db.QueryRow(s.ctx, `SELECT manifest FROM revision_asset_manifests WHERE hash=$1`, hash).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		e = os.ErrNotExist
	}
	if e != nil {
		return nil, e
	}
	var m assetManifest
	e = json.Unmarshal(raw, &m)
	return cloneAndSortAssetRefs(m.Items), e
}
func (s *postgresStore) AssetManifestExists(hash string) bool {
	_, e := s.LoadAssetManifest(hash)
	return hash != "" && e == nil
}
func (s *postgresStore) SaveRevision(r *Revision) error   { return s.save(r, false) }
func (s *postgresStore) UpdateRevision(r *Revision) error { return s.save(r, true) }
func (s *postgresStore) save(r *Revision, update bool) error {
	if r == nil || r.ID == "" || r.PageID == "" || r.CreatedAt.IsZero() {
		return fmt.Errorf("revision id, page id and created_at are required")
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	if update {
		tag, err := s.db.Exec(s.ctx, `UPDATE revisions SET content_hash=$3,asset_manifest_hash=$4,metadata=$5 WHERE page_id=$1 AND id=$2`, r.PageID, r.ID, r.ContentHash, r.AssetManifestHash, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return os.ErrNotExist
		}
	} else {
		key := revisionFileTimestamp(r.CreatedAt) + "_" + r.ID + ".json"
		if _, e = s.db.Exec(s.ctx, `INSERT INTO revisions(page_id,id,sort_key,content_hash,asset_manifest_hash,metadata) VALUES($1,$2,$3,$4,$5,$6)`, r.PageID, r.ID, key, r.ContentHash, r.AssetManifestHash, raw); e != nil {
			return e
		}
	}
	_, e = s.db.Exec(s.ctx, `UPDATE pages SET current_revision_id=(SELECT id FROM revisions WHERE page_id=$1 ORDER BY sort_key DESC LIMIT 1) WHERE id=$1`, r.PageID)
	return e
}
func (s *postgresStore) ListRevisions(pageID string) ([]*Revision, error) {
	r, _, e := s.ListRevisionsPage(pageID, "", 0)
	return r, e
}
func (s *postgresStore) ListRevisionsPage(pageID, cursor string, limit int) ([]*Revision, string, error) {
	if e := validateStorageID(pageID); e != nil {
		return nil, "", e
	}
	cursor = strings.TrimSpace(cursor)
	var take *int
	if limit > 0 {
		n := limit + 1
		take = &n
	}
	rows, e := s.db.Query(s.ctx, `SELECT sort_key,metadata FROM revisions WHERE page_id=$1 AND ($2='' OR (sort_key<$2 AND EXISTS(SELECT 1 FROM revisions WHERE page_id=$1 AND sort_key=$2))) ORDER BY sort_key DESC LIMIT $3`, pageID, cursor, take)
	if e != nil {
		return nil, "", e
	}
	defer rows.Close()
	revs := []*Revision{}
	keys := []string{}
	for rows.Next() {
		var key string
		var raw []byte
		if e = rows.Scan(&key, &raw); e != nil {
			return nil, "", e
		}
		r := &Revision{}
		if e = json.Unmarshal(raw, r); e != nil {
			return nil, "", e
		}
		revs = append(revs, r)
		keys = append(keys, key)
	}
	if e = rows.Err(); e != nil {
		return nil, "", e
	}
	next := ""
	if limit > 0 && len(revs) > limit {
		next = keys[limit-1]
		revs = revs[:limit]
	}
	return revs, next, nil
}
func (s *postgresStore) GetLatestRevision(pageID string) (*Revision, error) {
	r, _, e := s.ListRevisionsPage(pageID, "", 1)
	if e != nil || len(r) == 0 {
		return nil, e
	}
	return r[0], nil
}
func (s *postgresStore) GetRevision(pageID, id string) (*Revision, error) {
	var raw []byte
	e := s.db.QueryRow(s.ctx, `SELECT metadata FROM revisions WHERE page_id=$1 AND id=$2`, pageID, strings.TrimSpace(id)).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		e = os.ErrNotExist
	}
	if e != nil {
		return nil, e
	}
	r := &Revision{}
	e = json.Unmarshal(raw, r)
	return r, e
}
func (s *postgresStore) PruneRevisions(pageID string, keep int) error {
	if keep <= 0 {
		return nil
	}
	_, e := s.db.Exec(s.ctx, `DELETE FROM revisions WHERE page_id=$1 AND id IN(SELECT id FROM revisions WHERE page_id=$1 ORDER BY sort_key DESC OFFSET $2)`, pageID, keep)
	return e
}
func (s *postgresStore) DeleteContentBlobIfUnreferenced(pageID, hash string) error {
	_, e := s.db.Exec(s.ctx, `DELETE FROM revision_contents c WHERE page_id=$1 AND content_hash=$2 AND NOT EXISTS(SELECT 1 FROM revisions r WHERE r.page_id=c.page_id AND r.content_hash=c.content_hash)`, pageID, hash)
	return e
}
func (s *postgresStore) DeletePageRevisions(pageID string) error {
	if _, e := s.db.Exec(s.ctx, `UPDATE pages SET current_revision_id=NULL WHERE id=$1`, pageID); e != nil {
		return e
	}
	_, e := s.db.Exec(s.ctx, `DELETE FROM revisions WHERE page_id=$1`, pageID)
	return e
}
func (s *postgresStore) contentBlobPath(pageID, hash string) string {
	return "postgres:revision_contents/" + pageID + "/" + hash
}
func (s *postgresStore) assetManifestPath(hash string) string {
	return "postgres:revision_asset_manifests/" + hash
}
