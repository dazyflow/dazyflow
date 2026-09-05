// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestContentHashed(t *testing.T) {
	hashed := []string{
		"index--GYgTU5i.js", "FlowEditor-BdAWitUj.js", "index-BAqKSaoq.css",
		"xterm-DIO9kghb.js.map", "logo-A1b2C3d4.svg",
	}
	for _, name := range hashed {
		if !contentHashed(name) {
			t.Errorf("contentHashed(%q) = false, want true", name)
		}
	}
	plain := []string{
		"index.html", "favicon.png", "logo.png", "github.svg", "bimi.svg",
		"connect-your-form.html", "short-Ab.js", "no-extension",
		// Seven hash characters, not eight — must not be trusted.
		"index-BAqKSaq.css",
		// The separator has to be a dash.
		"indexBAqKSaqx.css",
	}
	for _, name := range plain {
		if contentHashed(name) {
			t.Errorf("contentHashed(%q) = true, want false", name)
		}
	}
}

// The policy is only sound if every file the build actually emits into
// assets/ is content-hashed — an unhashed one would be pinned in browsers
// for a year. Guards against a future Vite config that stops hashing.
func TestContentHashed_CoversTheRealBuildOutput(t *testing.T) {
	dir := filepath.Join("..", "web", "dist", "assets")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no built assets to check: %v", err)
	}
	if len(entries) == 0 {
		t.Skip("assets/ is empty")
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !contentHashed(e.Name()) {
			t.Errorf("built asset %q is not content-hashed, so it must not be served immutable", e.Name())
		}
	}
}

func TestWebDistHandler_CacheControlByAssetKind(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.html":                 "<!doctype html><script src=/assets/app-Ab3dEf9x.js></script>",
		"favicon.png":                "png",
		"assets/app-Ab3dEf9x.js":     "console.log(1)",
		"assets/style-Zz9yXw8v.css":  "body{}",
		"assets/app-Ab3dEf9x.js.map": "{}",
		"vendor-Ab3dEf9x.js":         "console.log(2)",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := webDistHandler(root)

	const immutable = "public, max-age=31536000, immutable"
	for path, want := range map[string]string{
		"/assets/app-Ab3dEf9x.js":     immutable,
		"/assets/style-Zz9yXw8v.css":  immutable,
		"/assets/app-Ab3dEf9x.js.map": immutable,
		// Outside assets/ a hash-shaped name earns nothing: only the
		// build's own output directory is trusted to be hashed.
		"/vendor-Ab3dEf9x.js": "public, max-age=3600",
		"/favicon.png":        "public, max-age=3600",
		// The shell, and the client-side routes that fall back to it,
		// name this build's assets, so they revalidate. (FileServer
		// redirects /index.html to /, so / is the path that serves it.)
		"/":          "no-cache",
		"/flows/abc": "no-cache",
	} {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest("GET", path, nil))
		if rw.Code != http.StatusOK {
			t.Errorf("GET %s = %d", path, rw.Code)
			continue
		}
		if got := rw.Header().Get("Cache-Control"); got != want {
			t.Errorf("GET %s Cache-Control = %q, want %q", path, got, want)
		}
	}
}

// An immutable asset still has to be served correctly, and a missing one
// must still 404 rather than acquiring a year-long lifetime.
func TestWebDistHandler_MissingAssetStillNotFound(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := webDistHandler(root)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, httptest.NewRequest("GET", "/assets/gone-Ab3dEf9x.js", nil))
	if rw.Code != http.StatusNotFound {
		t.Fatalf("missing asset = %d, want 404", rw.Code)
	}
	if cc := rw.Header().Get("Cache-Control"); cc == "public, max-age=31536000, immutable" {
		t.Fatal("a 404 must not be cached for a year")
	}
}
