package transfer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InspectLegacy inspects a stopped legacy instance without opening the source
// with a writable storage constructor. It does not connect to PostgreSQL. This
// is preflight inspection only: Valid is not a claim that an import committed.
func InspectLegacy(ctx context.Context, root string) (Report, error) {
	r := Report{Counts: map[string]int{"pages": 0, "sections": 0, "revisions": 0, "users": 0, "sessions": 0, "api_keys": 0, "email_tokens": 0, "favorites": 0, "user_settings": 0, "assets": 0}, Issues: []Issue{}}
	files, err := Inventory(ctx, root)
	if err != nil {
		return r, err
	}
	r.SourceHash = inventoryHash(files)
	pages, err := inspectPages(ctx, root, &r)
	if err != nil {
		return r, err
	}
	revisions, err := inspectRevisions(ctx, root, files, pages, &r)
	if err != nil {
		return r, err
	}
	users := map[string]bool{"system": true}
	pageIDs := map[string]bool{"root": true}
	for _, p := range pages {
		pageIDs[p.Node.ID] = true
	}
	for _, source := range []struct{ file, table string }{
		{"users.db", "users"}, {"sessions.db", "sessions"}, {"api_keys.db", "api_keys"}, {"email_tokens.db", "email_tokens"}, {"favorites.db", "favorites"}, {"usersettings.db", "user_settings"},
	} {
		path := filepath.Join(root, source.file)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return r, err
		}
		err := withSQLiteCopy(ctx, path, func(db *sql.DB) error {
			// Table names come only from this fixed list, never workspace values.
			rows, err := db.QueryContext(ctx, "SELECT * FROM "+source.table)
			if err != nil {
				return fmt.Errorf("expected canonical table cannot be read")
			}
			defer rows.Close()
			columns, err := rows.Columns()
			if err != nil {
				return err
			}
			for rows.Next() {
				values := make([]any, len(columns))
				args := make([]any, len(columns))
				for i := range values {
					args[i] = &values[i]
				}
				if err := rows.Scan(args...); err != nil {
					return fmt.Errorf("canonical row cannot be read")
				}
				r.Counts[source.table]++
				stringsByColumn := map[string]string{}
				for i, column := range columns {
					switch v := values[i].(type) {
					case string:
						stringsByColumn[column] = v
					case []byte:
						stringsByColumn[column] = string(v)
					}
				}
				if source.table == "users" {
					users[stringsByColumn["id"]] = true
				} else {
					if id := stringsByColumn["user_id"]; id != "" && !users[id] {
						r.Issues = append(r.Issues, Issue{Code: "orphan_user_relation", Path: source.file, Severity: "warning", Message: "existing relation references an absent user; no ID was changed"})
					}
					if source.table == "favorites" && !pageIDs[stringsByColumn["page_id"]] {
						r.Issues = append(r.Issues, Issue{Code: "orphan_favorite", Path: source.file, Severity: "warning", Message: "favorite references an absent page; no relation was removed"})
					}
				}
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("canonical table read failed")
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return r, ctx.Err()
			}
			r.issue("unreadable_sqlite", source.file, "SQLite integrity or canonical table inspection failed; source was not repaired")
		}
	}
	for _, p := range pages {
		for _, id := range []string{p.Node.Metadata.CreatorID, p.Node.Metadata.LastAuthorID} {
			if id != "" && !users[id] {
				r.Issues = append(r.Issues, Issue{Code: "unresolved_author", Path: p.Source, Severity: "warning", Message: "page references an absent author; original ID retained"})
			}
		}
	}
	for _, rev := range revisions {
		for _, id := range []string{rev.Revision.AuthorID, rev.Revision.CreatorID, rev.Revision.LastAuthorID} {
			if id != "" && !users[id] {
				r.Issues = append(r.Issues, Issue{Code: "unresolved_author", Path: filepath.ToSlash(filepath.Join(".leafwiki/revisions", rev.Revision.PageID, rev.SortKey)), Severity: "warning", Message: "revision references an absent author; original ID retained"})
			}
		}
	}
	for _, file := range files {
		if strings.HasPrefix(file.Path, "assets/") {
			r.Counts["assets"]++
			parts := strings.Split(file.Path, "/")
			if len(parts) > 2 && !pageIDs[parts[1]] {
				r.Issues = append(r.Issues, Issue{Code: "orphan_asset", Path: file.Path, Severity: "warning", Message: "asset directory has no current page; file must not be discarded"})
			}
		}
	}
	for _, name := range []string{"branding.json", "public-access.json"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return r, err
		}
		if !json.Valid(raw) {
			r.issue("malformed_settings", name, "persistent settings JSON is invalid; not repaired")
		} else {
			r.Counts["settings"]++
		}
	}
	inspectAssetReferences(root, files, pages, &r)
	after, err := Inventory(ctx, root)
	if err != nil {
		return r, err
	}
	if inventoryHash(after) != r.SourceHash {
		r.issue("source_changed", "", "workspace changed during inspection; stop the legacy instance and inspect again")
	}
	return r, nil
}
