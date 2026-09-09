package transfer

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/perber/wiki/internal/branding"
	"github.com/perber/wiki/internal/core/markdown"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func inspectAssetReferences(root string, files []FileRecord, pages []legacyPage, r *Report) {
	present := map[string]bool{}
	for _, f := range files {
		present[f.Path] = true
	}
	parser := goldmark.New().Parser()
	for _, page := range pages {
		if page.Raw == nil {
			continue
		}
		_, body, _, err := markdown.ParseFrontmatter(*page.Raw)
		if err != nil {
			continue
		}
		doc := parser.Parse(text.NewReader([]byte(body)))
		_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			var destination string
			switch node := n.(type) {
			case *ast.Link:
				destination = string(node.Destination)
			case *ast.Image:
				destination = string(node.Destination)
			default:
				return ast.WalkContinue, nil
			}
			parsed, err := url.Parse(destination)
			if err != nil || parsed.Host != "" || parsed.Scheme != "" {
				return ast.WalkContinue, nil
			}
			path := strings.TrimPrefix(parsed.Path, "/")
			if strings.HasPrefix(path, "assets/") && !present[path] {
				r.issue("missing_current_asset", page.Source, "Markdown references a missing current attachment; reference was not rewritten")
			}
			return ast.WalkContinue, nil
		})
	}
	if raw, err := os.ReadFile(filepath.Join(root, "branding.json")); err == nil {
		var cfg branding.BrandingConfig
		if json.Unmarshal(raw, &cfg) != nil {
			r.issue("malformed_settings", "branding.json", "branding settings do not match the existing schema")
		} else {
			for _, name := range []string{cfg.LogoFile, cfg.FaviconFile} {
				if name != "" && !present["branding/"+name] {
					r.issue("missing_branding_asset", "branding.json", "configured branding file is missing")
				}
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "public-access.json")); err == nil {
		var cfg struct {
			Enabled bool `json:"enabled"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			r.issue("malformed_settings", "public-access.json", "public access settings do not match the existing schema")
		}
	}
}
