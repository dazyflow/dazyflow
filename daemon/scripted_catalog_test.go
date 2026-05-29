package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine/containerdrop"
	"git.sr.ht/~klahr/hazy-flow/engine/jsdrop"
)

// testScriptedCatalog builds a catalog wired to the real Node runtime — the
// Extract hook (manifest reading, needed by InstallDrop/preview) and a Run hook
// (process-tier execution) — exactly as configureScriptedRuntime does in the
// daemon. There is no in-process JS engine, so install tests that read a drop's
// manifest need Node; the test skips when `node` is absent.
func testScriptedCatalog(t *testing.T) *jsdrop.Catalog {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	drophost, err := filepath.Abs("../engine/containerdrop/nodehost/drophost.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(drophost); err != nil {
		t.Fatalf("drop host not found: %v", err)
	}
	cat := jsdrop.NewCatalog()
	cat.Extract = containerdrop.NodeManifestExtractor(node, drophost)
	cat.Run = func(m core.Manifest, jsESM string, _ bool) core.Transport {
		return containerdrop.NewTransport(
			m,
			containerdrop.DropRef{ID: m.ID, Argv: []string{node, drophost}, Source: []byte(jsESM)},
			containerdrop.ProcessRunner{},
			containerdrop.Host{},
		)
	}
	return cat
}
