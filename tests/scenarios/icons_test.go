// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/dazyflow/dazyflow/engine"
)

var iconRegistryBlock = regexp.MustCompile(`(?s)const iconRegistry[^{]*\{(.*?)\n\};`)

var iconRegistryKey = regexp.MustCompile(`(?m)^\s*"?([A-Za-z0-9-]+)"?\s*:`)

// A drop's Manifest.Icon is a name the FRONTEND has to know: iconFor() looks it
// up in icons.tsx's iconRegistry and, finding nothing, falls back to the step's
// CATEGORY default. So an icon named in Go and missing there is an error
// nowhere — no build fails, no console warning — and the step just wears the
// wrong glyph. Thirty of them had drifted out of sync that way, which is how a
// regex step, a phone step and a folder step all ended up sharing one icon.
//
// The registry is parsed out of the TSX rather than restated here, because a
// second copy of the list would drift in exactly the way this guards against.
func registeredIcons(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join("..", "..", "web", "src", "icons.tsx")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block := iconRegistryBlock.FindSubmatch(src)
	if block == nil {
		t.Fatalf("could not find the iconRegistry object literal in %s — if it was renamed or reshaped, update this guard rather than deleting it", path)
	}
	registered := map[string]bool{}
	for _, m := range iconRegistryKey.FindAllSubmatch(block[1], -1) {
		registered[string(m[1])] = true
	}
	// Guard the guard: a regex that silently matched nothing would make this
	// test pass while checking nothing at all.
	if len(registered) < 20 {
		t.Fatalf("parsed only %d registry keys from %s — the regex no longer matches the file's shape", len(registered), path)
	}
	return registered
}

func TestManifestIconsAreRegisteredInTheWebUI(t *testing.T) {
	registered := registeredIcons(t)

	var missing []string
	checked := 0
	for id, m := range engine.Default.Manifests() {
		if m.Icon == "" {
			continue
		}
		checked++
		if !registered[m.Icon] {
			missing = append(missing, m.Icon+" (declared by "+id+")")
		}
	}
	if checked == 0 {
		t.Fatal("no manifest declared an icon — the drop catalog didn't register, so this guard is passing vacuously")
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("manifest icon %s is not in web/src/icons.tsx's iconRegistry — the step falls back to its category glyph instead", name)
	}
}

// A template's icon runs through the same iconFor() as a step's, so an
// unregistered name is just as invisible — the gallery card wears a plain box
// and nothing says why. The guard above covers drop manifests only, and a
// template may name an icon no drop uses (package-search on the price watcher),
// so the gallery needs its own pass over the same registry.
func TestTemplateIconsAreRegisteredInTheWebUI(t *testing.T) {
	registered := registeredIcons(t)

	path := filepath.Join("..", "..", "web", "public", "templates", "index.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var index struct {
		Templates []struct {
			ID   string `json:"id"`
			Icon string `json:"icon"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(index.Templates) == 0 {
		t.Fatalf("no templates parsed from %s — this guard would pass vacuously", path)
	}

	var missing []string
	for _, tpl := range index.Templates {
		if tpl.Icon == "" {
			continue // no icon named is a deliberate default, not a typo
		}
		if !registered[tpl.Icon] {
			missing = append(missing, tpl.Icon+" (declared by "+tpl.ID+")")
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("template icon %s is not in web/src/icons.tsx's iconRegistry — the gallery card falls back to a plain box", name)
	}
}
