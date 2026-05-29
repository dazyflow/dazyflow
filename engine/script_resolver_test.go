package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine/jsdrop"
)

// pingManifestJSON is the manifest the Node drop host would emit for a ping
// drop (camelCase, as authored). Routing it through jsdrop.ParseManifest in the
// stub extractor exercises the real camelCase→core.Manifest mapping.
const pingManifestJSON = `{
  "id": "ping_drop", "version": "1.0", "label": "Ping",
  "summary": "GET a URL with a bearer token and return the JSON body.",
  "integration": "Demo",
  "outputs": [{ "port": "out" }],
  "requiresConnections": [{ "kind": "secret", "name": "TOKEN" }],
  "examples": [{ "title": "ping", "params": { "url": "https://api.example.com/ping" } }]
}`

const pingDropTS = `
export default {
  manifest: { id: "ping_drop", version: "1.0", summary: "ping" },
  async run(ctx) { return { out: {} }; },
};`

// Full resolution path: register a scripted drop in a jsdrop.Catalog (manifest
// read via the Node Extract hook, here stubbed), expose it through NodeResolver,
// and confirm it surfaces in the merged manifest set with the camelCase manifest
// correctly mapped, and resolves like a native drop. Execution-through-Node is
// covered by engine/containerdrop (official_via_node_test.go).
func TestScriptedDrop_ResolvesAndMapsManifestViaNodeResolver(t *testing.T) {
	cat := jsdrop.NewCatalog()
	cat.Extract = func(name, source string) (core.Manifest, error) {
		return jsdrop.ParseManifest([]byte(pingManifestJSON))
	}
	cat.Run = func(m core.Manifest, _ string, _ bool) core.Transport { return scriptEcho{m: m} }
	if err := cat.Register("ping_drop.ts", pingDropTS); err != nil {
		t.Fatalf("register: %v", err)
	}

	resolver := &NodeResolver{Script: cat}

	// 1. It shows up in the merged manifest set ListDrops/the canvas reads, with
	//    the camelCase manifest correctly mapped to core.Manifest.
	m, ok := resolver.Manifests()["ping_drop"]
	if !ok {
		t.Fatal("ping_drop missing from resolver.Manifests()")
	}
	if m.ExecutionModel != core.ExecutionBatch {
		t.Errorf("ExecutionModel = %q, want batch (default)", m.ExecutionModel)
	}
	if m.Provider != "script" {
		t.Errorf("Provider = %q, want script", m.Provider)
	}
	if len(m.Outputs) != 1 || m.Outputs[0].Port != "out" {
		t.Errorf("Outputs mapping off: %#v", m.Outputs)
	}
	if len(m.RequiresConnections) != 1 || m.RequiresConnections[0].Name != "TOKEN" {
		t.Errorf("RequiresConnections mapping off: %#v", m.RequiresConnections)
	}

	// 2. It resolves through the NodeResolver exactly like a native drop.
	tr, err := resolver.Resolve(context.Background(), "ping_drop")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tr.Manifest().ID != "ping_drop" {
		t.Errorf("resolved manifest ID = %q, want ping_drop", tr.Manifest().ID)
	}
}

// A scripted drop missing required manifest metadata is rejected at register
// time, same as a native drop missing Summary/Examples. The Node extractor's
// ParseManifest rejects a missing summary, and AddPrebuilt re-checks the
// registration minimums.
func TestScriptedDrop_RejectsIncompleteManifest(t *testing.T) {
	cat := jsdrop.NewCatalog()
	// Extractor returns a manifest with no summary — must be rejected.
	cat.Extract = func(name, source string) (core.Manifest, error) {
		return core.Manifest{ID: "bad", Version: "1.0",
			Examples: []core.ParamsExample{{Title: "x"}}}, nil
	}
	if err := cat.Register("bad.ts", `export default { manifest: {}, run() {} };`); err == nil {
		t.Fatal("expected registration to fail without a summary")
	}
}
