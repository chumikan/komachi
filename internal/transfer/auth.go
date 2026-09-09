package transfer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type legacyTable struct {
	Name    string
	Columns []string
	Rows    [][]any
}

var authTables = []struct{ file, table, columns string }{
	{"users.db", "users", "id,username,password,email,role,created_at,totp_secret_encrypted,totp_enabled,totp_recovery_codes_json,totp_enabled_at,totp_last_reset_at,must_set_password"},
	{"sessions.db", "sessions", "id,user_id,token_type,created_at,expires_at,revoked_at"},
	{"api_keys.db", "api_keys", "id,name,user_id,prefix,key_hash,role,expires_at,created_by,created_at,last_used_at,revoked_at"},
	{"email_tokens.db", "email_tokens", "id,token_hash,user_id,purpose,created_at,expires_at,consumed_at"},
	{"favorites.db", "favorites", "user_id,page_id,created_at"},
	{"usersettings.db", "user_settings", "user_id,language,autosave,date_format,time_format,updated_at"},
}

func readAuth(ctx context.Context, root string, report *Report) ([]legacyTable, error) {
	var result []legacyTable
	for _, spec := range authTables {
		source := filepath.Join(root, spec.file)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		table := legacyTable{Name: spec.table, Columns: strings.Split(spec.columns, ",")}
		err := withSQLiteCopy(ctx, source, func(db *sql.DB) error {
			tables, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
			if err != nil {
				return err
			}
			for tables.Next() {
				var name string
				if err := tables.Scan(&name); err != nil {
					tables.Close()
					return err
				}
				if name != spec.table {
					tables.Close()
					return fmt.Errorf("unrecognized canonical SQLite table")
				}
			}
			if err := tables.Err(); err != nil {
				tables.Close()
				return err
			}
			tables.Close()
			probe, err := db.QueryContext(ctx, "SELECT * FROM "+spec.table+" LIMIT 0")
			if err != nil {
				return err
			}
			columns, err := probe.Columns()
			probe.Close()
			if err != nil {
				return err
			}
			if strings.Join(columns, ",") != spec.columns {
				// Column order can differ after the existing additive SQLite upgrades.
				known := map[string]bool{}
				for _, c := range table.Columns {
					known[c] = true
				}
				if len(columns) != len(known) {
					return fmt.Errorf("unsupported legacy columns")
				}
				for _, c := range columns {
					if !known[c] {
						return fmt.Errorf("unsupported legacy column")
					}
				}
			}
			selects := append([]string(nil), table.Columns...)
			for i, c := range table.Columns {
				if (spec.table == "users" && (c == "created_at" || c == "totp_enabled_at" || c == "totp_last_reset_at")) || (spec.table == "favorites" && c == "created_at") || (spec.table == "user_settings" && c == "updated_at") {
					selects[i] = "CAST(" + c + " AS TEXT)"
				}
			}
			rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(selects, ",")+" FROM "+spec.table)
			if err != nil {
				return err
			}
			defer rows.Close()
			originalColumns := append([]string(nil), table.Columns...)
			if spec.table == "users" || spec.table == "favorites" {
				table.Columns = append(table.Columns, "created_at_legacy")
			}
			if spec.table == "favorites" {
				table.Columns = append(table.Columns, "created_at_submicro")
			}
			if spec.table == "user_settings" {
				table.Columns = append(table.Columns, "updated_at_legacy")
			}
			for rows.Next() {
				values := make([]any, len(originalColumns))
				args := make([]any, len(values))
				for i := range values {
					args[i] = &values[i]
				}
				if err := rows.Scan(args...); err != nil {
					return err
				}
				for i, c := range originalColumns {
					if b, ok := values[i].([]byte); ok {
						values[i] = string(b)
					}
					if c == "created_at" && (spec.table == "users" || spec.table == "favorites") {
						original := values[i]
						values = append(values, original)
						nanos := 0
						if original != nil {
							raw, ok := original.(string)
							if !ok {
								return fmt.Errorf("unsupported timestamp")
							}
							parsed, err := parseLegacyTime(raw)
							if err != nil {
								return err
							}
							values[i] = parsed.Truncate(time.Microsecond)
							nanos = parsed.Nanosecond() % 1000
						}
						if spec.table == "favorites" {
							values = append(values, nanos)
						}
					}
					if spec.table == "user_settings" && c == "autosave" {
						n, ok := values[i].(int64)
						if !ok || (n != 0 && n != 1) {
							return fmt.Errorf("invalid autosave boolean")
						}
						values[i] = n == 1
					}
					if spec.table == "user_settings" && c == "updated_at" {
						raw, ok := values[i].(string)
						if !ok {
							return fmt.Errorf("invalid user settings timestamp")
						}
						parsed, err := parseLegacyTime(raw)
						if err != nil {
							return err
						}
						values[i] = parsed.Format(time.RFC3339Nano)
						values = append(values, raw)
					}
				}
				table.Rows = append(table.Rows, values)
			}
			return rows.Err()
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			report.issue("unsupported_auth_data", spec.file, "canonical SQLite schema, timestamp or value cannot be imported without alteration")
			continue
		}
		result = append(result, table)
	}
	return result, nil
}

func parseLegacyTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if v, err := time.Parse(layout, raw); err == nil {
			return v.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized legacy timestamp; no replacement generated")
}
