// Package transfer implements read-only legacy inspection and explicit data
// transfer. Normal page/auth repositories do not depend on this package.
package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Issue struct {
	Code     string `json:"code"`
	Path     string `json:"path,omitempty"`
	Severity string `json:"severity"`
	// Messages describe the condition, never source credentials or record values.
	Message string `json:"message"`
}
type Report struct {
	Counts        map[string]int `json:"counts"`
	Issues        []Issue        `json:"issues"`
	SourceHash    string         `json:"source_hash"`
	Committed     bool           `json:"committed"`
	CommitUnknown bool           `json:"commit_unknown,omitempty"`
}

func (r *Report) issue(code, path, message string) {
	r.Issues = append(r.Issues, Issue{Code: code, Path: filepath.ToSlash(path), Severity: "error", Message: message})
}
func (r *Report) Valid() bool {
	for _, i := range r.Issues {
		if i.Severity == "error" {
			return false
		}
	}
	return true
}

type FileRecord struct {
	Path       string    `json:"path"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

func hashBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Inventory uses Lstat/WalkDir and rejects links rather than following a legacy
// workspace path outside its root. It performs no chmod, mkdir or writeback.
func Inventory(ctx context.Context, root string) ([]FileRecord, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("workspace must be a real directory")
	}
	var records []FileRecord
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsupported symbolic link: %s", filepath.ToSlash(rel))
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported special file: %s", filepath.ToSlash(rel))
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, readErr := io.Copy(h, contextReader{ctx: ctx, reader: f})
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		records = append(records, FileRecord{Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, ModifiedAt: info.ModTime().UTC()})
		return nil
	})
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	return records, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func inventoryHash(records []FileRecord) string {
	var b strings.Builder
	for _, r := range records {
		fmt.Fprintf(&b, "%d:%s:%d:%s:%s\n", len(r.Path), r.Path, r.Size, r.SHA256, r.ModifiedAt.Format(time.RFC3339Nano))
	}
	return hashBytes([]byte(b.String()))
}
