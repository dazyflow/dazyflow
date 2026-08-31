// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package scenarios

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/dazyflow/dazyflow/engine"
)

// iconRegistryBlock isolates the object literal assigned to iconRegistry, so a
// name appearing anywhere else in icons.tsx (a lucide import, categoryFallback,
// a comment) can't be mistaken for a registered key.
var iconRegistryBlock = regexp.MustCompile(`(?s)const iconRegistry[^{]*\{(.*?)\n\};`)

// iconRegistryKey matches one `key: Component,` line, quoted or not.
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
func TestManifestIconsAreRegisteredInTheWebUI(t *testing.T) {
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

	var missing []string
	checked := 0
	for id, m := range engine.Default.Manifests() {
		// No declared icon is fine and intended: iconFor falls back by
		// category, which is what categoryFallback exists for.
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
