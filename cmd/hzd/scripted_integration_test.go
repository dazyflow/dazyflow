package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/containerdrop"
	"git.sr.ht/~klahr/hazy-flow/engine/jsdrop"
	hfnet "git.sr.ht/~klahr/hazy-flow/drops/net"
)

// These tests reconstruct the exact runtime wiring main()/configureScriptedRuntime
// builds — the real scriptedDropDoer, an OAuth-style token resolver that reads
// the tenant from ctx (mirroring OAuthRegistry.GetOAuthToken), and a real
// FSQuota.Reserve, all reached by the drop ONLY through the broker — then drive
// scripted drops through a NodeResolver with a tenant-bearing context and real
// sandbox roots. They execute on the production Node runtime (process tier), so
// they lock the daemon-edge composition + the broker capability seam against
// regressions. Skipped when `node` is not installed.

// nodeDropHost returns the absolute path to drophost.mjs from this test's
// package dir, skipping the test if `node` is unavailable.
func nodeDropHost(t *testing.T) (node, drophost string) {
	t.Helper()
	n, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	abs, err := filepath.Abs("../../engine/containerdrop/nodehost/drophost.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("drop host not found: %v", err)
	}
	return n, abs
}

// newWiredCatalog mirrors configureScriptedRuntime: it wires the Run hook to the
// Node drop host (process tier) and the Extract hook to the Node manifest
// reader, with the broker Host carrying the guarded fetch doer, the tenant-aware
// token resolver, and the FSQuota reserver. limits is the per-tenant byte budget
// for the test's quota (nil = unlimited).
func newWiredCatalog(t *testing.T, limits map[string]int64, sawTenant *string) *jsdrop.Catalog {
	t.Helper()
	quota, err := daemon.NewFSQuota(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	node, drophost := nodeDropHost(t)

	tokens := func(ctx context.Context, provider, account string) (string, error) {
		// This is exactly what oauthRegistry.GetOAuthToken depends on: the
		// tenant must ride in the execution context.
		tn, ok := core.TenantFromContext(ctx)
		if !ok {
			return "", context.Canceled // surfaces as a token error
		}
		if sawTenant != nil {
			*sawTenant = tn
		}
		return "tok-" + tn + "-" + provider + "-" + account, nil
	}
	host := containerdrop.Host{
		HTTP:  scriptedDropDoer{client: hfnet.SafeHTTPClient(30*time.Second, false)},
		Token: tokens,
		Files: func(job core.Job) jsdrop.FileStore { return jsdrop.NewJobFileStore(job, quota.Reserve) },
	}
	argv := []string{node, drophost}

	cat := jsdrop.NewCatalog()
	cat.Run = func(m core.Manifest, jsESM string, _ bool) core.Transport {
		return containerdrop.NewTransport(
			m,
			containerdrop.DropRef{ID: m.ID, Argv: argv, Source: []byte(jsESM), RestrictEgress: len(m.Egress) > 0, Egress: m.Egress},
			containerdrop.ProcessRunner{},
			host,
		)
	}
	cat.Extract = containerdrop.NodeManifestExtractor(node, drophost)
	return cat
}

// auth.token + files round-trip through the engine, with the tenant flowing
// from the execution context all the way to the token resolver.
func TestDaemonScripted_AuthAndFiles(t *testing.T) {
	const src = `
export default {
  manifest: {
    id: "profile_cache", version: "1.0", label: "Profile cache",
    summary: "Resolve a token, stash it to scratch, read it back.",
    requiresConnections: [{ kind: "oauth", name: "github" }],
    outputs: [{ port: "out" }],
    examples: [{ title: "x", params: {} }],
  },
  async run(ctx) {
    const tok = await ctx.auth.token("github");
    await ctx.files.write("scratch://token.txt", tok);
    const back = await ctx.files.readText("scratch://token.txt");
    return { out: { tok, back } };
  },
};`
	var sawTenant string
	cat := newWiredCatalog(t, nil, &sawTenant) // unlimited quota
	if err := cat.Register("profile_cache.ts", src); err != nil {
		t.Fatalf("register: %v", err)
	}
	resolver := &engine.NodeResolver{Script: cat}
	tr, err := resolver.Resolve(context.Background(), "profile_cache")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	scratch := t.TempDir()
	ctx := core.WithTenant(context.Background(), "acme")
	progress := make(chan core.Progress, 8)
	res, err := tr.Execute(ctx, core.Job{
		ID:          "job-1",
		NodeID:      "n1",
		Tenant:      "acme",
		ScratchRoot: scratch,
		Params:      map[string]any{"account": "work"},
	}, progress)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %v", res.Status, res.Error)
	}

	if sawTenant != "acme" {
		t.Errorf("token resolver saw tenant %q, want acme (tenant must flow from ctx)", sawTenant)
	}
	out, _ := res.Output["out"].Inline.(map[string]any)
	if out["tok"] != "tok-acme-github-work" {
		t.Errorf("tok = %v, want tok-acme-github-work (provider+account threaded)", out["tok"])
	}
	if out["back"] != out["tok"] {
		t.Errorf("readback %v != written %v", out["back"], out["tok"])
	}
	// The bytes really landed in the job's scratch root.
	if b, err := os.ReadFile(filepath.Join(scratch, "token.txt")); err != nil || string(b) != "tok-acme-github-work" {
		t.Errorf("scratch file = %q, err = %v", b, err)
	}
}

// files.write over the tenant's FSQuota is refused via the real reserver, and
// surfaces as a clean error Result with nothing written.
func TestDaemonScripted_QuotaRefusal(t *testing.T) {
	const src = `
export default {
  manifest: { id: "hog", version: "1.0", label: "Hog",
    summary: "Write more than the tenant quota allows.",
    examples: [{ title: "x", params: {} }] },
  async run(ctx) { await ctx.files.write("scratch://big.txt", "x".repeat(50)); return {}; },
};`
	cat := newWiredCatalog(t, map[string]int64{"acme": 4}, nil) // 4-byte budget
	if err := cat.Register("hog.ts", src); err != nil {
		t.Fatalf("register: %v", err)
	}
	resolver := &engine.NodeResolver{Script: cat}
	tr, _ := resolver.Resolve(context.Background(), "hog")

	scratch := t.TempDir()
	ctx := core.WithTenant(context.Background(), "acme")
	res, err := tr.Execute(ctx, core.Job{
		ID: "job-2", NodeID: "n1", Tenant: "acme", ScratchRoot: scratch,
	}, make(chan core.Progress, 4))
	if err != nil {
		t.Fatalf("execute returned infra error: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "quota_exceeded") {
		t.Fatalf("error = %v, want quota_exceeded", res.Error)
	}
	if _, statErr := os.Stat(filepath.Join(scratch, "big.txt")); statErr == nil {
		t.Error("file was written despite quota refusal")
	}
}

// The production fetch doer (SafeHTTPClient with allowPrivate=false) must
// refuse a loopback URL — proving the SSRF guard is actually wired into the
// scripted-drop network path, not just available.
func TestDaemonScripted_FetchSSRFGuarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const src = `
export default {
  manifest: { id: "loopback", version: "1.0", label: "Loopback",
    summary: "Fetch a loopback URL; the guard must refuse it.",
    outputs: [{ port: "out" }],
    examples: [{ title: "x", params: {} }] },
  async run(ctx) { const r = await ctx.fetch(ctx.params.url); return { out: r.status }; },
};`
	cat := newWiredCatalog(t, nil, nil)
	if err := cat.Register("loopback.ts", src); err != nil {
		t.Fatalf("register: %v", err)
	}
	resolver := &engine.NodeResolver{Script: cat}
	tr, _ := resolver.Resolve(context.Background(), "loopback")

	res, err := tr.Execute(context.Background(), core.Job{
		ID: "job-3", NodeID: "n1", Params: map[string]any{"url": srv.URL},
	}, make(chan core.Progress, 4))
	if err != nil {
		t.Fatalf("execute returned infra error: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("loopback fetch was allowed (status %q) — SSRF guard not wired into the doer", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "ssrf") {
		t.Fatalf("error = %v, want an ssrf_blocked refusal", res.Error)
	}
	t.Logf("loopback fetch correctly refused: %s", res.Error.Message)
}
