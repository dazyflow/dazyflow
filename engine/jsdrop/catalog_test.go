package jsdrop

import (
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// LoadDir is the path the daemon uses at startup
// (HAZYFLOW_SCRIPTED_DROPS_DIR): read every *.ts/*.js file in a dir and
// register it. This exercises disk → transpile → register, with the manifest
// read through the Node Extract hook (stubbed here so the test needs no node).
func TestCatalog_LoadDirRegistersScriptedDrops(t *testing.T) {
	dir := t.TempDir()
	const src = `
export default {
  manifest: {
    id: "hello_drop", version: "1.0", label: "Hello",
    summary: "Return a greeting.",
    outputs: [{ port: "out" }],
    examples: [{ title: "hi", params: {} }],
  },
  run() { return { out: "hello" }; },
};`
	if err := os.WriteFile(filepath.Join(dir, "hello.ts"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	// A non-drop file in the same dir must be ignored, not errored on.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}

	cat := NewCatalog()
	// Stand in for the Node manifest extractor + runtime.
	cat.Extract = func(name, source string) (core.Manifest, error) {
		return manifestFor("hello_drop", "1.0"), nil
	}
	cat.Run = func(m core.Manifest, _ string, _ bool) core.Transport { return echoTransport{m: m} }
	if err := cat.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	mans := cat.Manifests()
	if _, ok := mans["hello_drop"]; !ok {
		t.Fatalf("hello_drop not registered; have %v", keys(mans))
	}
	if len(mans) != 1 {
		t.Errorf("registered %d drops, want 1 (README.md must be ignored)", len(mans))
	}
	if _, ok := cat.Get("hello_drop"); !ok {
		t.Error("Get(hello_drop) returned not-ok after LoadDir")
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
