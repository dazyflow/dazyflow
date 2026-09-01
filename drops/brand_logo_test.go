// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A manifest's BrandLogo is a URL the browser fetches from web/public, so the
// two halves of it live in different languages and nothing connects them: the
// Go side names a path, the file sits in the frontend tree, and a name that
// matches nothing fails the way a missing image always does — silently, as a
// card with a blank where its logo goes. That is how sheets_update_cells
// shipped pointing at /brands/google-sheets.svg while its three siblings and
// the actual file were all /brands/sheets.svg: correct-looking, plausible, and
// wrong only in a way you find by looking at the card.
//
// Checked here rather than in the frontend because the Go manifests are the
// side that can be wrong — the file either exists or it doesn't.
func TestAllDrops_BrandLogosResolve(t *testing.T) {
	root := repoRootForBrands(t)
	for _, d := range allDrops(t) {
		logo := strings.TrimSpace(d.manifest.BrandLogo)
		if logo == "" {
			continue // no brand mark: the drop renders its lucide Icon instead
		}
		t.Run(d.id, func(t *testing.T) {
			if !strings.HasPrefix(logo, "/") {
				t.Fatalf("BrandLogo %q is not an absolute URL path", logo)
			}
			path := filepath.Join(root, "web", "public", filepath.FromSlash(logo))
			if _, err := os.Stat(path); err != nil {
				t.Errorf("BrandLogo %q has no file at web/public%s — "+
					"add the asset, or point at one that exists", logo, logo)
			}
		})
	}
}

// repoRootForBrands walks up from the cwd looking for go.mod.
func repoRootForBrands(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
