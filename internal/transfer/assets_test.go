package transfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectionRejectsMissingAssets(t *testing.T) {
	for _, fixture := range []struct{ name, path, content, code string }{
		{"current", "root/page.md", pageFixture("page-id", "Page") + "\n![attachment](/assets/page-id/missing.png)", "missing_current_asset"},
		{"branding", "branding.json", `{"logoFile":"missing.png"}`, "missing_branding_asset"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			source, _ := revisionFixture(t)
			writeFixture(t, source, fixture.path, fixture.content)
			report, err := InspectLegacy(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			if report.Valid() {
				t.Fatal("accepted missing attachment")
			}
			for _, issue := range report.Issues {
				if issue.Code == fixture.code {
					return
				}
			}
			t.Fatal("missing attachment was not reported")
		})
	}
}

func TestBackupRejectsSymlinkedIgnoreRoot(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeFixture(t, outside, ".leafwikiignore", "hidden\n")
	if err := os.Symlink(outside, filepath.Join(dir, "root")); err != nil {
		t.Fatal(err)
	}
	if _, err := persistentInventory(context.Background(), dir); err == nil {
		t.Fatal("silently omitted symlinked ignore settings")
	}
}
