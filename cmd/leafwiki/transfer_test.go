package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/perber/wiki/internal/test_utils"
)

func TestTransferCommandHelp(t *testing.T) {
	for _, args := range [][]string{{"database", "import-legacy", "--help"}, {"database", "backup", "--help"}, {"restore-snapshot", "--help"}} {
		cmd := newRootCommand()
		var output bytes.Buffer
		cmd.Writer = &output
		cmd.ErrWriter = &output
		if err := cmd.Run(context.Background(), append([]string{"leafwiki"}, args...)); err != nil {
			t.Fatal(err)
		}
		if args[0] == "restore-snapshot" && (!strings.Contains(output.String(), "source you trust") || !strings.Contains(output.String(), "--replace")) {
			t.Fatal("restore help omits trust/destructive replacement boundary")
		}
		if len(args) > 1 && args[1] == "import-legacy" && !strings.Contains(output.String(), "--dry-run") {
			t.Fatal("import help omits dry-run")
		}
	}
}

func TestPostgresImportCommandDryRunAndRetry(t *testing.T) {
	ctx := context.Background()
	pg, cfg := test_utils.PostgresStore(t)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEAFWIKI_DATABASE_URL", cfg.URL)
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "root"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "schema.json"), []byte(`{"version":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	raw := "---\nleafwiki_id: original-id\nleafwiki_title: Original\nleafwiki_created_at: '2025-01-01T00:00:00Z'\nleafwiki_updated_at: '2025-01-01T00:00:00Z'\n---\n本文\n"
	if err := os.WriteFile(filepath.Join(source, "root", "original.md"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	run := func(dry bool) (string, error) {
		cmd := newRootCommand()
		var output bytes.Buffer
		cmd.Writer = &output
		cmd.ErrWriter = &output
		args := []string{"leafwiki", "database", "import-legacy", "--source", source, "--target", target}
		if dry {
			args = append(args, "--dry-run")
		}
		err := cmd.Run(ctx, args)
		return output.String(), err
	}
	out, err := run(true)
	if err != nil || !strings.Contains(out, `"committed":false`) {
		t.Fatalf("dry-run CLI: %v", err)
	}
	var count int
	if err := pg.DB().QueryRow(ctx, `SELECT count(*) FROM pages WHERE id<>'root'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("CLI dry-run committed")
	}
	out, err = run(false)
	if err != nil || !strings.Contains(out, `"committed":true`) {
		t.Fatalf("real CLI import: %v", err)
	}
	if _, err := run(false); err == nil {
		t.Fatal("CLI duplicate import succeeded")
	}
}
