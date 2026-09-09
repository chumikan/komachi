package transfer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/perber/wiki/internal/core/revision"
)

type legacyRevision struct {
	Revision revision.Revision
	Raw      json.RawMessage
	SortKey  string
	Content  string
	Manifest json.RawMessage
}

func validHash(hash string) bool {
	if len(hash) != 64 || hash != strings.ToLower(hash) {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func inspectRevisions(ctx context.Context, root string, files []FileRecord, pages []legacyPage, report *Report) ([]legacyRevision, error) {
	pageIDs := map[string]bool{}
	for _, p := range pages {
		pageIDs[p.Node.ID] = true
	}
	fileMap := map[string]FileRecord{}
	for _, f := range files {
		fileMap[f.Path] = f
	}
	var result []legacyRevision
	seen := map[string]map[string]string{}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(file.Path, ".leafwiki/revisions/") {
			continue
		}
		parts := strings.Split(file.Path, "/")
		if len(parts) != 4 || !strings.HasSuffix(parts[3], ".json") {
			report.issue("unsupported_revision_file", file.Path, "unrecognized revision layout")
			continue
		}
		if parts[3] == "_index.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.Path)))
		if err != nil {
			return nil, err
		}
		var rev revision.Revision
		if json.Unmarshal(raw, &rev) != nil || rev.ID == "" || rev.CreatedAt.IsZero() {
			report.issue("malformed_revision", file.Path, "revision metadata is invalid")
			continue
		}
		if rev.PageID != parts[2] || !pageIDs[rev.PageID] {
			report.issue("orphan_revision", file.Path, "revision has no matching current page")
			continue
		}
		if seen[rev.PageID] == nil {
			seen[rev.PageID] = map[string]string{}
		}
		if seen[rev.PageID][rev.ID] != "" {
			report.issue("duplicate_revision_id", file.Path, "revision ID occurs in more than one JSON file")
			continue
		}
		seen[rev.PageID][rev.ID] = parts[3]
		if !validHash(rev.ContentHash) || !validHash(rev.AssetManifestHash) {
			report.issue("invalid_revision_hash", file.Path, "content or manifest hash is not a SHA-256 identifier")
			continue
		}
		contentPath := filepath.ToSlash(filepath.Join(".leafwiki", "blobs", "content", rev.PageID, "sha256", rev.ContentHash[:2], rev.ContentHash))
		if _, ok := fileMap[contentPath]; !ok {
			contentPath = filepath.ToSlash(filepath.Join(".leafwiki", "blobs", "content", "sha256", rev.ContentHash[:2], rev.ContentHash))
		}
		contentFile, ok := fileMap[contentPath]
		if !ok {
			report.issue("missing_revision_blob", file.Path, "revision content blob is missing")
			continue
		}
		if contentFile.SHA256 != rev.ContentHash {
			report.issue("corrupt_revision_blob", contentPath, "content checksum does not match revision hash")
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(contentPath)))
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(content) || strings.ContainsRune(string(content), 0) {
			report.issue("unsupported_text", contentPath, "revision content is not representable as PostgreSQL UTF-8 TEXT")
			continue
		}
		manifestPath := filepath.ToSlash(filepath.Join(".leafwiki", "manifests", "assets", "sha256", rev.AssetManifestHash[:2], rev.AssetManifestHash+".json"))
		manifestFile, ok := fileMap[manifestPath]
		if !ok {
			report.issue("missing_asset_manifest", file.Path, "revision attachment manifest is missing")
			continue
		}
		if manifestFile.SHA256 != rev.AssetManifestHash {
			report.issue("corrupt_asset_manifest", manifestPath, "manifest checksum does not match revision hash")
			continue
		}
		manifest, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifestPath)))
		if err != nil {
			return nil, err
		}
		var assets struct {
			Items []revision.AssetRef `json:"items"`
		}
		if json.Unmarshal(manifest, &assets) != nil {
			report.issue("malformed_asset_manifest", manifestPath, "attachment manifest JSON is invalid")
			continue
		}
		assetNames := map[string]bool{}
		for _, asset := range assets.Items {
			name := strings.TrimSpace(asset.Name)
			if assetNames[name] {
				report.issue("duplicate_asset_name", manifestPath, "attachment manifest contains a repeated name")
			}
			assetNames[name] = true
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
				report.issue("invalid_asset_name", manifestPath, "attachment name cannot be restored safely")
				continue
			}
			if !validHash(asset.SHA256) {
				report.issue("invalid_asset_hash", manifestPath, "attachment hash is invalid")
				continue
			}
			blobPath := filepath.ToSlash(filepath.Join(".leafwiki", "blobs", "assets", "sha256", asset.SHA256[:2], asset.SHA256))
			blob, ok := fileMap[blobPath]
			if !ok {
				report.issue("missing_historical_asset", manifestPath, "attachment blob referenced by revision is missing")
				continue
			}
			if blob.SHA256 != asset.SHA256 || blob.Size != asset.SizeBytes {
				report.issue("corrupt_historical_asset", blobPath, "attachment blob checksum or size differs from manifest")
			}
		}
		result = append(result, legacyRevision{Revision: rev, Raw: raw, SortKey: parts[3], Content: string(content), Manifest: manifest})
		report.Counts["revisions"]++
	}
	for _, file := range files {
		if !strings.HasPrefix(file.Path, ".leafwiki/revisions/") || !strings.HasSuffix(file.Path, "/_index.json") {
			continue
		}
		parts := strings.Split(file.Path, "/")
		if len(parts) != 4 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.Path)))
		if err != nil {
			return nil, err
		}
		var index map[string]string
		if json.Unmarshal(raw, &index) != nil {
			report.issue("malformed_revision_index", file.Path, "revision lookup index is invalid")
			continue
		}
		for id, name := range index {
			if seen[parts[2]][id] != name {
				report.issue("inconsistent_revision_index", file.Path, "revision lookup index disagrees with history JSON")
				break
			}
		}
		for id, name := range seen[parts[2]] {
			if index[id] != name {
				report.issue("incomplete_revision_index", file.Path, "revision lookup index omits history JSON")
				break
			}
		}
	}
	return result, nil
}
