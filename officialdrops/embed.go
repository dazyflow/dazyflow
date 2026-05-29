// Package officialdrops ships Hazy Flow's first-party connectors as scripted
// (TypeScript) drops embedded in the binary and registered into the scripted
// catalog at boot. These REPLACE the former native Go connectors: an official
// drop is an ordinary scripted drop that runs on the exact same Node runtime as
// any third-party drop ("official == scripted"), with no compiled fast path.
package officialdrops

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/engine/jsdrop"
)

// excel_read.ts / excel_write.ts are generated (SheetJS bundled with the drop
// body); regenerate with `go generate ./officialdrops` after editing the
// vendored lib or the bodies under excelsrc/.
//go:generate go run ./excelsrc

// manifests.json maps each *.ts filename to the raw manifest JSON emitted by the
// Node drop host. Generated so boot registers official drops WITHOUT a Node
// spawn per drop. Regenerate with `go generate ./officialdrops` after editing
// any drop's manifest.
//go:generate go run ./manifestgen

//go:embed *.ts manifests.json
var fsys embed.FS

// Register adds every embedded drop to the catalog as a global-default drop
// (visible to every tenant), using its generate-time-embedded manifest — no
// Node manifest extraction on the boot path. Call once at boot. A mismatch
// between the embedded sources and manifests.json is an error (stale
// generation): run `go generate ./officialdrops`.
func Register(cat *jsdrop.Catalog) error {
	raw, err := fsys.ReadFile("manifests.json")
	if err != nil {
		return fmt.Errorf("read embedded manifests: %w", err)
	}
	var manifests map[string]json.RawMessage
	if err := json.Unmarshal(raw, &manifests); err != nil {
		return fmt.Errorf("decode embedded manifests: %w", err)
	}

	entries, err := fsys.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read embedded drops: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ts") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // deterministic registration order
	if len(names) != len(manifests) {
		return fmt.Errorf("manifests.json is stale: %d drops, %d manifests (run `go generate ./officialdrops`)", len(names), len(manifests))
	}

	for _, name := range names {
		mraw, ok := manifests[name]
		if !ok {
			return fmt.Errorf("manifests.json missing %q (run `go generate ./officialdrops`)", name)
		}
		man, err := jsdrop.ParseManifest(mraw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		src, err := fsys.ReadFile(name)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		// Official embedded drops are first-party code — trusted for the
		// relaxed egress default (no declared egress → process-wide policy).
		if _, _, err := cat.AddPrebuilt(name, string(src), man, true, true); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
