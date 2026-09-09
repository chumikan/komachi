package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/perber/wiki/internal/core/ignore"
	"github.com/perber/wiki/internal/core/markdown"
	"github.com/perber/wiki/internal/core/tree"
)

type legacyPage struct {
	Node   *tree.PageNode
	Raw    *string
	Source string
}

// inspectPages reads schema-5 files without running reconstruction, migrations,
// frontmatter serialization, or ID generation. Call only after Inventory has
// rejected symlinks/special files, with the legacy instance stopped.
func inspectPages(ctx context.Context, root string, report *Report) ([]legacyPage, error) {
	rootPath := filepath.Join(root, "root")
	info, err := os.Stat(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // an empty workspace has no persisted root metadata
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		report.issue("invalid_root", "root", "page root is not a directory")
		return nil, nil
	}
	schemaRaw, err := os.ReadFile(filepath.Join(root, "schema.json"))
	var schema tree.SchemaInfo
	if err != nil || json.Unmarshal(schemaRaw, &schema) != nil || schema.Version != tree.CurrentSchemaVersion {
		report.issue("unsupported_schema", "schema.json", "expected existing filesystem schema version 5; source was not upgraded")
		return nil, nil
	}
	rootNode := &tree.PageNode{ID: "root", Title: "root", Slug: "root", Kind: tree.NodeKindSection,
		Metadata: tree.PageMetadata{CreatedAt: info.ModTime().UTC(), UpdatedAt: info.ModTime().UTC(), CreatorID: "system", LastAuthorID: "system"}}
	pages := []legacyPage{{Node: rootNode, Source: "root"}}
	seen := map[string]bool{"root": true}
	ignoreCache := ignore.NewCache(rootPath)
	var visit func(string, *tree.PageNode) error
	visit = func(dir string, parent *tree.PageNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.SliceStable(entries, func(i, j int) bool {
			a, b := strings.ToLower(entries[i].Name()), strings.ToLower(entries[j].Name())
			if a == b {
				return entries[i].Name() < entries[j].Name()
			}
			return a < b
		})
		slugs := map[string]bool{}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			path := filepath.Join(dir, name)
			rel, _ := filepath.Rel(root, path)
			if strings.HasPrefix(name, ".") {
				continue
			}
			ignoreRel, _ := filepath.Rel(rootPath, path)
			if ig := ignoreCache.Get(dir); ig != nil && ig.Matches(filepath.ToSlash(ignoreRel), entry.IsDir()) {
				report.Issues = append(report.Issues, Issue{Code: "ignored_path", Path: filepath.ToSlash(rel), Severity: "warning", Message: "excluded by existing ignore rules"})
				continue
			}
			slug := name
			kind := tree.NodeKindSection
			if entry.IsDir() {
				path = filepath.Join(path, "index.md")
			} else {
				ext := filepath.Ext(name)
				if !strings.EqualFold(ext, ".md") {
					continue
				}
				slug = strings.TrimSuffix(name, ext)
				kind = tree.NodeKindPage
				if strings.EqualFold(slug, "index") {
					if parent.ID == "root" || name != "index.md" {
						report.issue("unrepresented_index", rel, "legacy root/case-variant index is not represented by the page tree")
					}
					continue
				}
			}
			if tree.NewSlugService().IsValidSlug(slug) != nil {
				report.issue("invalid_slug", rel, "existing filename cannot be represented as a page slug")
				continue
			}
			key := strings.ToLower(strings.TrimSpace(slug))
			if slugs[key] {
				report.issue("duplicate_slug", rel, "case-insensitive sibling slug collision")
				continue
			}
			slugs[key] = true
			raw, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				report.issue("missing_section_identity", rel, "section has no index.md with a persisted ID; no ID was generated")
				continue
			}
			if err != nil {
				return err
			}
			if !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
				report.issue("unsupported_text", rel, "Markdown is not representable as PostgreSQL UTF-8 TEXT")
				continue
			}
			md, err := markdown.NewMarkdownFileFromRaw(path, string(raw))
			if err != nil {
				report.issue("malformed_frontmatter", rel, "frontmatter cannot be parsed; source was not repaired")
				continue
			}
			fm := md.GetFrontmatter()
			if fm.WasRepaired() {
				report.issue("malformed_frontmatter", rel, "frontmatter requires repair; source was not repaired")
				continue
			}
			if strings.TrimSpace(fm.LeafWikiID) == "" {
				report.issue("missing_page_id", rel, "no persisted page ID; no ID was generated")
				continue
			}
			if strings.ContainsAny(fm.LeafWikiID, "/\\") || fm.LeafWikiID == "." || fm.LeafWikiID == ".." {
				report.issue("invalid_page_id", rel, "page ID cannot safely identify existing attachment/revision storage")
				continue
			}
			if seen[fm.LeafWikiID] {
				report.issue("duplicate_page_id", rel, "page ID already occurs in this workspace")
				continue
			}
			seen[fm.LeafWikiID] = true
			st, err := os.Stat(path)
			if err != nil {
				return err
			}
			metadata := tree.PageMetadata{CreatorID: fm.LeafWikiCreatorID, LastAuthorID: fm.LeafWikiLastAuthorID}
			if metadata.CreatorID == "" {
				metadata.CreatorID = "system"
			}
			if metadata.LastAuthorID == "" {
				metadata.LastAuthorID = "system"
			}
			valid := true
			for _, field := range []struct {
				raw  string
				dest *time.Time
			}{{fm.LeafWikiCreatedAt, &metadata.CreatedAt}, {fm.LeafWikiUpdatedAt, &metadata.UpdatedAt}} {
				if field.raw == "" {
					*field.dest = st.ModTime().UTC()
					report.Issues = append(report.Issues, Issue{Code: "legacy_mtime_fallback", Path: filepath.ToSlash(rel), Severity: "warning", Message: "missing metadata timestamp uses existing file mtime fallback without writeback"})
				} else {
					parsed, err := time.Parse(time.RFC3339Nano, field.raw)
					if err != nil {
						valid = false
						report.issue("invalid_timestamp", rel, "metadata timestamp is invalid; no replacement was generated")
					} else {
						*field.dest = parsed.UTC()
					}
				}
			}
			if !valid {
				continue
			}
			title, err := md.GetTitle()
			if err != nil {
				return err
			}
			node := &tree.PageNode{ID: fm.LeafWikiID, Title: title, Slug: slug, Kind: kind, Parent: parent, Position: len(parent.Children), Pinned: fm.LeafWikiPinned, Metadata: metadata}
			parent.Children = append(parent.Children, node)
			content := string(raw)
			pages = append(pages, legacyPage{Node: node, Raw: &content, Source: filepath.ToSlash(rel)})
			if kind == tree.NodeKindSection {
				report.Counts["sections"]++
				if err := visit(filepath.Dir(path), node); err != nil {
					return err
				}
			} else {
				report.Counts["pages"]++
			}
		}
		orderPath := filepath.Join(dir, ".order.json")
		orderRaw, err := os.ReadFile(orderPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, orderPath)
		var order struct {
			OrderedIDs []string `json:"ordered_ids"`
		}
		if json.Unmarshal(orderRaw, &order) != nil {
			report.issue("malformed_order", rel, "ordering JSON cannot be parsed")
			return nil
		}
		positions := map[string]int{}
		children := map[string]bool{}
		for _, c := range parent.Children {
			children[c.ID] = true
		}
		for i, id := range order.OrderedIDs {
			if _, ok := positions[id]; ok {
				report.Issues = append(report.Issues, Issue{Code: "duplicate_order_id", Path: filepath.ToSlash(rel), Severity: "warning", Message: "existing ordering uses first occurrence of a repeated ID"})
				continue
			}
			if !children[id] {
				report.Issues = append(report.Issues, Issue{Code: "orphan_order_id", Path: filepath.ToSlash(rel), Severity: "warning", Message: "ordering references a non-child; existing ordering ignores it"})
			}
			positions[id] = i
		}
		sort.SliceStable(parent.Children, func(i, j int) bool {
			a, ai := positions[parent.Children[i].ID]
			b, bi := positions[parent.Children[j].ID]
			if ai && bi {
				return a < b
			}
			return ai && !bi
		})
		for i, c := range parent.Children {
			c.Position = i
		}
		return nil
	}
	err = visit(rootPath, rootNode)
	return pages, err
}
