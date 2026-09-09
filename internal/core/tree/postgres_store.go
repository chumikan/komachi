package tree

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/perber/wiki/internal/core/markdown"
	"github.com/perber/wiki/internal/storage/postgres"
)

// postgresNodeStore reuses the existing Markdown transformations, replacing
// filesystem I/O only. Node metadata is small; Markdown is fetched on demand.
type postgresNodeStore struct {
	*NodeStore // legacy migration helpers are never called by the PostgreSQL tree
	db         postgres.DBTX
	ctx        context.Context
}

func (s *postgresNodeStore) load() (*PageNode, error) {
	rows, err := s.db.Query(s.ctx, `SELECT id, parent_id, title, slug, kind, position, pinned, metadata FROM pages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := map[string]*PageNode{}
	parents := map[string]string{}
	for rows.Next() {
		n := &PageNode{Children: []*PageNode{}}
		var parent *string
		var meta []byte
		if err := rows.Scan(&n.ID, &parent, &n.Title, &n.Slug, &n.Kind, &n.Position, &n.Pinned, &meta); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(meta, &n.Metadata); err != nil {
			return nil, err
		}
		nodes[n.ID] = n
		if parent != nil {
			parents[n.ID] = *parent
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	root := nodes["root"]
	if root == nil {
		return nil, ErrTreeNotLoaded
	}
	for id, parent := range parents {
		n := nodes[id]
		n.Parent = nodes[parent]
		if n.Parent == nil {
			return nil, ErrParentNotFound
		}
		n.Parent.Children = append(n.Parent.Children, n)
	}
	for _, n := range nodes {
		sort.Slice(n.Children, func(i, j int) bool {
			a, b := n.Children[i], n.Children[j]
			if a.Position == b.Position {
				return a.ID < b.ID
			}
			return a.Position < b.Position
		})
	}
	return root, nil
}

func (s *postgresNodeStore) save(n *PageNode, raw *string, insert bool) error {
	meta, err := json.Marshal(n.Metadata)
	if err != nil {
		return err
	}
	var parent *string
	if n.Parent != nil {
		parent = &n.Parent.ID
	}
	if insert {
		_, err = s.db.Exec(s.ctx, `INSERT INTO pages(id,parent_id,title,slug,kind,position,pinned,metadata,content_markdown) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, n.ID, parent, n.Title, n.Slug, n.Kind, n.Position, n.Pinned, meta, raw)
	} else {
		_, err = s.db.Exec(s.ctx, `UPDATE pages SET parent_id=$2,title=$3,slug=$4,kind=$5,position=$6,pinned=$7,metadata=$8,content_markdown=$9 WHERE id=$1`, n.ID, parent, n.Title, n.Slug, n.Kind, n.Position, n.Pinned, meta, raw)
	}
	return err
}

func (s *postgresNodeStore) raw(n *PageNode) (*string, error) {
	var raw *string
	err := s.db.QueryRow(s.ctx, `SELECT content_markdown FROM pages WHERE id=$1`, n.ID).Scan(&raw)
	return raw, err
}
func (s *postgresNodeStore) markdown(n *PageNode) (*markdown.MarkdownFile, error) {
	raw, err := s.raw(n)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return markdown.NewMarkdownFile("", "", markdown.Frontmatter{}), nil
	}
	return markdown.NewMarkdownFileFromRaw("", *raw)
}
func (s *postgresNodeStore) write(n *PageNode, m *markdown.MarkdownFile, insert bool) error {
	s.syncManagedFrontmatter(m, n)
	raw, err := markdown.BuildMarkdownWithFrontmatter(m.GetFrontmatter(), m.GetContent())
	if err != nil {
		return err
	}
	return s.save(n, &raw, insert)
}
func (s *postgresNodeStore) CreatePage(parent, n *PageNode) error {
	return s.write(n, markdown.NewMarkdownFile("", "# "+n.Title+"\n", markdown.Frontmatter{}), true)
}
func (s *postgresNodeStore) CreateSection(parent, n *PageNode) error {
	return s.write(n, markdown.NewMarkdownFile("", "", markdown.Frontmatter{}), true)
}
func (s *postgresNodeStore) UpsertContent(n *PageNode, content string) error {
	m, err := s.markdown(n)
	if err != nil {
		return err
	}
	m.SetContent(content)
	return s.write(n, m, false)
}
func (s *postgresNodeStore) UpsertContentPreservingFrontmatter(n *PageNode, content string) error {
	m, err := s.markdown(n)
	if err != nil {
		return err
	}
	if err = m.SetRawContentPreservingManagedFrontmatter(content); err != nil {
		return err
	}
	return s.write(n, m, false)
}
func (s *postgresNodeStore) UpsertContentAndMetadata(n *PageNode, body string, tags []string, properties map[string]string) error {
	m, err := s.markdown(n)
	if err != nil {
		return err
	}
	m.SetContent(body)
	existing := m.GetFrontmatter().ExtraFields
	extra := make(map[string]interface{})
	for k, v := range existing {
		if k == "tags" {
			continue
		}
		switch v.(type) {
		case string, map[string]interface{}:
		default:
			extra[k] = v
		}
	}
	for k, v := range properties {
		extra[k] = v
	}
	if tags != nil {
		list := make([]interface{}, len(tags))
		for i, t := range tags {
			list[i] = t
		}
		extra["tags"] = list
	} else if v, ok := existing["tags"]; ok {
		extra["tags"] = v
	}
	m.SetExtraFields(extra)
	return s.write(n, m, false)
}
func (s *postgresNodeStore) ReadPageRaw(n *PageNode) (string, error) {
	r, e := s.raw(n)
	if r == nil {
		return "", e
	}
	return *r, e
}
func (s *postgresNodeStore) ReadPageAndRaw(n *PageNode) (string, string, error) {
	r, e := s.ReadPageRaw(n)
	if e != nil {
		return "", "", e
	}
	m, e := markdown.NewMarkdownFileFromRaw("", r)
	if e != nil {
		return "", "", e
	}
	return m.GetContent(), r, nil
}
func (s *postgresNodeStore) ReadPageContent(n *PageNode) (string, error) {
	c, _, e := s.ReadPageAndRaw(n)
	return c, e
}
func (s *postgresNodeStore) SyncFrontmatterIfExists(n *PageNode) error {
	raw, e := s.raw(n)
	if e != nil {
		return e
	}
	if raw == nil {
		return s.save(n, nil, false)
	}
	m, e := markdown.NewMarkdownFileFromRaw("", *raw)
	if e != nil {
		return e
	}
	return s.write(n, m, false)
}
func (s *postgresNodeStore) SaveChildOrder(n *PageNode) error {
	for _, child := range n.Children {
		if _, e := s.db.Exec(s.ctx, `UPDATE pages SET position=$2 WHERE id=$1`, child.ID, child.Position); e != nil {
			return e
		}
	}
	return nil
}
func (s *postgresNodeStore) RenameNode(n *PageNode, slug string) error {
	_, e := s.db.Exec(s.ctx, `UPDATE pages SET slug=$2 WHERE id=$1`, n.ID, slug)
	return e
}
func (s *postgresNodeStore) MoveNode(n, parent *PageNode) error {
	_, e := s.db.Exec(s.ctx, `UPDATE pages SET parent_id=$2 WHERE id=$1`, n.ID, parent.ID)
	return e
}
func (s *postgresNodeStore) DeletePage(n *PageNode) error {
	_, e := s.db.Exec(s.ctx, `DELETE FROM pages WHERE id=$1`, n.ID)
	return e
}
func (s *postgresNodeStore) DeleteSection(n *PageNode) error { return s.DeletePage(n) }
func (s *postgresNodeStore) ConvertNode(n *PageNode, kind NodeKind) error {
	if kind != NodeKindPage && kind != NodeKindSection {
		return &InvalidOpError{Op: "ConvertNode", Reason: fmt.Sprintf("unknown target kind: %q", kind)}
	}
	if kind == NodeKindPage && n.HasChildren() {
		return ErrPageHasChildren
	}
	m, e := s.markdown(n)
	if e != nil {
		return e
	}
	n.Kind = kind
	return s.write(n, m, false)
}
func (s *postgresNodeStore) SetPinnedFrontmatter(n *PageNode, pinned bool) (string, error) {
	raw, e := s.raw(n)
	if e != nil {
		return "", e
	}
	if raw == nil {
		return "", &InvalidOpError{Op: "SetPinnedFrontmatter", Reason: "section has no index file and cannot be pinned"}
	}
	m, e := s.markdown(n)
	if e != nil {
		return "", e
	}
	m.SetLeafWikiPinned(pinned)
	n.Pinned = pinned
	e = s.write(n, m, false)
	return m.GetContent(), e
}
