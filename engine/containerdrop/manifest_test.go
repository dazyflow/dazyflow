package containerdrop

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// NodeManifestExtractor reads a drop's manifest by running the SAME Node host
// (--emit-manifest) that executes it, and maps it through jsdrop.ParseManifest —
// the install-time gate. This asserts the round-trip + the field mapping +
// the rejection of an incomplete manifest. Skips without node.
func TestNodeManifestExtractor(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	node, _ := exec.LookPath("node")
	drophost, err := filepath.Abs("nodehost/drophost.mjs")
	if err != nil {
		t.Fatal(err)
	}
	extract := NodeManifestExtractor(node, drophost)

	const src = `export default {
		manifest: {
			id: "probe", version: "2.1.0", summary: "reads a thing.",
			outputs: [{ port: "out" }],
			requiresConnections: [{ kind: "oauth", name: "github" }],
			egress: ["api.github.com"],
			examples: [{ title: "x", params: {} }],
		},
		async run(ctx) { return { out: {} }; },
	};`
	m, err := extract("probe.ts", src)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if m.ID != "probe" || m.Version != "2.1.0" {
		t.Errorf("id/version mapping off: %q@%q", m.ID, m.Version)
	}
	if m.Provider != "script" {
		t.Errorf("Provider = %q, want script (default applied)", m.Provider)
	}
	if len(m.RequiresConnections) != 1 || m.RequiresConnections[0].Name != "github" {
		t.Errorf("requiresConnections mapping off: %#v", m.RequiresConnections)
	}
	if len(m.Egress) != 1 || m.Egress[0] != "api.github.com" {
		t.Errorf("egress mapping off: %#v", m.Egress)
	}

	// An incomplete manifest (no summary) is rejected by ParseManifest.
	if _, err := extract("bad.ts", `export default { manifest: { id: "bad", version: "1.0" }, run() {} };`); err == nil {
		t.Error("a manifest with no summary should be rejected")
	}
}
