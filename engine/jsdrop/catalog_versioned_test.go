package jsdrop

import (
	"context"
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// dropSrc builds a minimal valid drop source with a given id + version. The
// catalog no longer reads the manifest from source (that's the Node runtime's
// job); the source only has to transpile, and tests pass the manifest to
// AddPrebuilt explicitly via manifestFor.
func dropSrc(id, version string) string {
	return `export default { manifest: {
		id: "` + id + `", version: "` + version + `", label: "` + id + `",
		summary: "test drop.", outputs: [{ port: "out" }],
		examples: [{ title: "x", params: {} }],
	}, run() { return { out: "` + id + `@` + version + `" }; } };`
}

// manifestFor mirrors what the Node extractor would return for dropSrc.
func manifestFor(id, version string) core.Manifest {
	return core.Manifest{
		ID: id, Version: version, Label: id, Summary: "test drop.",
		Outputs:  []core.Port{{Port: "out"}},
		Examples: []core.ParamsExample{{Title: "x", Params: json.RawMessage("{}")}},
	}
}

// echoTransport is the stand-in the Run hook returns in tests that only assert
// resolution/visibility (not execution): its Manifest() echoes what was added.
type echoTransport struct{ m core.Manifest }

func (e echoTransport) Manifest() core.Manifest { return e.m }
func (e echoTransport) Execute(context.Context, core.Job, chan<- core.Progress) (core.Result, error) {
	return core.Result{Status: core.StatusOK}, nil
}

func mustAdd(t *testing.T, c *Catalog, id, version string, global bool) {
	t.Helper()
	// A drop can't resolve without a Run hook (no in-process engine), so wire a
	// trivial echo hook unless the test set its own.
	if c.Run == nil {
		c.Run = func(m core.Manifest, _ string, _ bool) core.Transport { return echoTransport{m: m} }
	}
	if _, _, err := c.AddPrebuilt(id+version, dropSrc(id, version), manifestFor(id, version), global, false); err != nil {
		t.Fatalf("AddPrebuilt %s@%s: %v", id, version, err)
	}
}

// Install is per-tenant and resolution is exact-pin: an un-installed version
// in the base does not resolve, and one tenant's install is invisible to
// another.
func TestCatalog_PerTenantInstallAndExactPin(t *testing.T) {
	c := NewCatalog()
	mustAdd(t, c, "stripe", "1.0.0", false)
	mustAdd(t, c, "stripe", "2.0.0", false)

	// In the base but installed by nobody → invisible.
	if _, ok := c.GetForTenant("acme", "stripe", "1.0.0"); ok {
		t.Fatal("resolved a version no tenant has installed")
	}

	if err := c.Install("acme", "stripe", "1.0.0"); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Exact pin resolves; the other base version does not (not installed).
	if tr, ok := c.GetForTenant("acme", "stripe", "1.0.0"); !ok || tr.Manifest().Version != "1.0.0" {
		t.Errorf("acme can't resolve its installed 1.0.0 (ok=%v)", ok)
	}
	if _, ok := c.GetForTenant("acme", "stripe", "2.0.0"); ok {
		t.Error("acme resolved un-installed 2.0.0")
	}

	// Tenant isolation: globex installed nothing.
	if _, ok := c.GetForTenant("globex", "stripe", "1.0.0"); ok {
		t.Error("globex sees acme's install — tenant isolation broken")
	}
}

// An install-update adds a newer version without disturbing the older pin: the
// palette advances to latest, but a graph pinned to the old version still
// resolves it exactly.
func TestCatalog_UpdateKeepsOldPinResolvable(t *testing.T) {
	c := NewCatalog()
	mustAdd(t, c, "stripe", "1.0.0", false)
	mustAdd(t, c, "stripe", "2.0.0", false)
	if err := c.Install("acme", "stripe", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	// Latest-installed (version "") = 1.0.0 so far.
	if tr, ok := c.GetForTenant("acme", "stripe", ""); !ok || tr.Manifest().Version != "1.0.0" {
		t.Fatalf("latest before update = %v", ok)
	}

	if err := c.Install("acme", "stripe", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	// Palette now shows 2.0.0…
	if got := c.ManifestsForTenant("acme")["stripe"].Version; got != "2.0.0" {
		t.Errorf("palette latest = %q, want 2.0.0", got)
	}
	// …but the 1.0.0 pin a running graph holds still resolves exactly.
	if tr, ok := c.GetForTenant("acme", "stripe", "1.0.0"); !ok || tr.Manifest().Version != "1.0.0" {
		t.Error("old 1.0.0 pin stopped resolving after 2.0.0 install")
	}
}

// Global-default drops (Add global=true — how official/boot-loaded drops ship)
// are visible to every tenant with no per-tenant install.
func TestCatalog_GlobalDefaultVisibleToAll(t *testing.T) {
	c := NewCatalog()
	mustAdd(t, c, "gmail", "1.0.0", true)
	for _, tn := range []string{"acme", "globex", ""} {
		if _, ok := c.GetForTenant(tn, "gmail", "1.0.0"); !ok {
			t.Errorf("global default invisible to tenant %q", tn)
		}
	}
	if _, ok := c.ManifestsForTenant("acme")["gmail"]; !ok {
		t.Error("global default missing from a tenant's palette")
	}
}

// ManifestsForTenant is scoped: a tenant sees its own installs + globals, not
// another tenant's installs.
func TestCatalog_ManifestsForTenantScoped(t *testing.T) {
	c := NewCatalog()
	mustAdd(t, c, "official", "1.0.0", true) // global
	mustAdd(t, c, "acme_only", "1.0.0", false)
	if err := c.Install("acme", "acme_only", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	acme := c.ManifestsForTenant("acme")
	if _, ok := acme["official"]; !ok {
		t.Error("acme missing the global drop")
	}
	if _, ok := acme["acme_only"]; !ok {
		t.Error("acme missing its own installed drop")
	}

	globex := c.ManifestsForTenant("globex")
	if _, ok := globex["acme_only"]; ok {
		t.Error("globex sees acme_only — palette not tenant-scoped")
	}
	if _, ok := globex["official"]; !ok {
		t.Error("globex missing the global drop")
	}
}

func TestCatalog_InstallUnknownVersionErrors(t *testing.T) {
	c := NewCatalog()
	mustAdd(t, c, "a", "1.0.0", false)
	if err := c.Install("acme", "a", "9.9.9"); err == nil {
		t.Fatal("install of an absent version should error")
	}
}

// The same (id,version) can't be added twice, but a second *version* of the
// same id is fine — that's what enables pinning across updates.
func TestCatalog_AddDedupePerVersion(t *testing.T) {
	c := NewCatalog()
	mustAdd(t, c, "a", "1.0.0", false)
	if _, _, err := c.AddPrebuilt("dup", dropSrc("a", "1.0.0"), manifestFor("a", "1.0.0"), false, false); err == nil {
		t.Error("duplicate (id,version) should error")
	}
	if _, _, err := c.AddPrebuilt("a2", dropSrc("a", "2.0.0"), manifestFor("a", "2.0.0"), false, false); err != nil {
		t.Errorf("a second version of the same id should be allowed: %v", err)
	}
}

// A drop without a version can't be added — versions are required for install
// + pinning.
func TestCatalog_AddRequiresVersion(t *testing.T) {
	c := NewCatalog()
	man := core.Manifest{ID: "nov", Label: "No version", Summary: "missing version.",
		Examples: []core.ParamsExample{{Title: "x", Params: json.RawMessage("{}")}}}
	if _, _, err := c.AddPrebuilt("nov.ts", dropSrc("nov", ""), man, false, false); err == nil {
		t.Fatal("AddPrebuilt should reject a manifest with no version")
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "2.0.0", true},
		{"1.9.0", "1.10.0", true}, // numeric, not lexical
		{"2.0.0", "1.0.0", false},
		{"1.0.0", "1.0.0", false},
		{"1.0", "1.0.1", true},
		{"1.0.0", "1.0", false},
	}
	for _, tc := range cases {
		if got := versionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
