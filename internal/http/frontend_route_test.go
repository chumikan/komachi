package http

import "testing"

func TestFrontendRouteBoundary(t *testing.T) {
	for _, path := range []string{"/", "/ja", "/ja/", "/ja/top-level-domain", "/ja/folder/page", "/ja/e/page", "/ja/history/page", "/ja/p/id/slug", "/login", "/settings", "/settings/users", "/users", "/forgot-password", "/reset-password", "/accept-invite"} {
		if !isFrontendRoute(path) {
			t.Errorf("missing frontend route %s", path)
		}
	}
	for _, path := range []string{"/top-level-domain", "/e/page", "/history/page", "/p/id", "/en/page", "/japan/page", "/api/health", "/api/pages", "/assets/image.png", "/static/main.js", "/branding/logo.png"} {
		if isFrontendRoute(path) {
			t.Errorf("unexpected frontend route %s", path)
		}
	}
}
