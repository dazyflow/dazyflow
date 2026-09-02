// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package llm

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

type fakeProvider struct {
	gotKey   string
	gotModel string
	reply    string
	jerr     *core.JobError
}

func (f *fakeProvider) Call(_ context.Context, apiKey string, req Request) (Result, *core.JobError) {
	f.gotKey, f.gotModel = apiKey, req.Model
	if f.jerr != nil {
		return Result{}, f.jerr
	}
	return Result{Text: f.reply}, nil
}

func TestRegistryAndGenerate(t *testing.T) {
	fp := &fakeProvider{reply: "<h1>hi</h1>"}
	Register(ProviderInfo{Name: "faketest", Integration: "FakeTest", DefaultModel: "fm-1", Provider: fp})

	if p, ok := Get("faketest"); !ok || p.Integration != "FakeTest" {
		t.Fatalf("Get(faketest) failed: %+v ok=%v", p, ok)
	}

	res, err := Generate(context.Background(), "faketest", "KEY", Request{UserText: "x"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if res.Text != "<h1>hi</h1>" {
		t.Errorf("text = %q", res.Text)
	}
	if fp.gotKey != "KEY" {
		t.Errorf("api key not passed through, got %q", fp.gotKey)
	}
	if fp.gotModel != "fm-1" {
		t.Errorf("model not defaulted to provider default, got %q", fp.gotModel)
	}

	if _, err := Generate(context.Background(), "does-not-exist", "k", Request{}); err == nil {
		t.Error("unknown provider should error")
	}
}

func TestGenerateSurfacesProviderError(t *testing.T) {
	Register(ProviderInfo{
		Name: "fakeerr", Integration: "FakeErr", DefaultModel: "m",
		Provider: &fakeProvider{jerr: &core.JobError{Code: "x", Message: "boom from provider"}},
	})
	_, err := Generate(context.Background(), "fakeerr", "k", Request{})
	if err == nil || !strings.Contains(err.Error(), "boom from provider") {
		t.Fatalf("want provider message surfaced, got %v", err)
	}
}

func TestRegisterIsIdempotentOnName(t *testing.T) {
	before := len(RegisteredNames())
	Register(ProviderInfo{Name: "dupe", Integration: "Dupe", Provider: &fakeProvider{}})
	Register(ProviderInfo{Name: "dupe", Integration: "Dupe2", Provider: &fakeProvider{}})
	after := len(RegisteredNames())
	if after != before+1 {
		t.Fatalf("re-registering the same name should not add a second entry: before=%d after=%d", before, after)
	}
	if p, _ := Get("dupe"); p.Integration != "Dupe2" {
		t.Errorf("re-register should replace, got integration %q", p.Integration)
	}
}

// TestRegistered_OrderAndContents exercises Registered(), which lists
// providers in registration order (distinct from RegisteredNames, which
// sorts). We register two fresh names and assert the slice preserves the
// order they were added and carries the full ProviderInfo.
func TestRegistered_OrderAndContents(t *testing.T) {
	// Capture the baseline so other tests' registrations don't break us.
	baseline := indexByName(Registered())

	Register(ProviderInfo{Name: "covzeta", Integration: "CovZeta", DefaultModel: "z1", Provider: &fakeProvider{}})
	Register(ProviderInfo{Name: "covalpha", Integration: "CovAlpha", DefaultModel: "a1", Provider: &fakeProvider{}})

	got := Registered()

	// Both new providers present, with their info intact.
	idx := indexByName(got)
	for _, name := range []string{"covzeta", "covalpha"} {
		p, ok := idx[name]
		if !ok {
			t.Fatalf("Registered() missing %q", name)
		}
		if p.Provider == nil {
			t.Errorf("%q: Provider should be carried through", name)
		}
	}
	if idx["covzeta"].Integration != "CovZeta" || idx["covalpha"].Integration != "CovAlpha" {
		t.Errorf("integration fields not carried: %+v", idx)
	}

	// covzeta was registered before covalpha; Registered() preserves that.
	posZeta := positionInOrder(got, "covzeta")
	posAlpha := positionInOrder(got, "covalpha")
	if posZeta < 0 || posAlpha < 0 {
		t.Fatalf("positions: zeta=%d alpha=%d", posZeta, posAlpha)
	}
	if posZeta >= posAlpha {
		t.Errorf("registration order not preserved: zeta at %d should precede alpha at %d", posZeta, posAlpha)
	}

	// The new registrations only added entries (no shrink).
	if len(got) < len(baseline)+2 {
		t.Errorf("Registered() len=%d, want at least %d", len(got), len(baseline)+2)
	}
}

func TestRegistered_ReRegisterKeepsPosition(t *testing.T) {
	Register(ProviderInfo{Name: "covdup", Integration: "First", DefaultModel: "m", Provider: &fakeProvider{}})
	first := Registered()
	posBefore := positionInOrder(first, "covdup")

	// Re-registering the same name replaces in place; it must NOT append a
	// duplicate or change its slot in registration order.
	Register(ProviderInfo{Name: "covdup", Integration: "Second", DefaultModel: "m", Provider: &fakeProvider{}})
	second := Registered()

	if count := countName(second, "covdup"); count != 1 {
		t.Fatalf("covdup appears %d times after re-register, want 1", count)
	}
	if posAfter := positionInOrder(second, "covdup"); posAfter != posBefore {
		t.Errorf("re-register moved position: before=%d after=%d", posBefore, posAfter)
	}
	idx := indexByName(second)
	if idx["covdup"].Integration != "Second" {
		t.Errorf("re-register should replace info, got %q", idx["covdup"].Integration)
	}
}

func indexByName(ps []ProviderInfo) map[string]ProviderInfo {
	m := make(map[string]ProviderInfo, len(ps))
	for _, p := range ps {
		m[p.Name] = p
	}
	return m
}

func positionInOrder(ps []ProviderInfo, name string) int {
	for i, p := range ps {
		if p.Name == name {
			return i
		}
	}
	return -1
}

func countName(ps []ProviderInfo, name string) int {
	n := 0
	for _, p := range ps {
		if p.Name == name {
			n++
		}
	}
	return n
}

// TestByIntegration covers the lookup the daemon's catalog layer uses: it
// starts from a drop manifest, which names the INTEGRATION ("Gemini") rather
// than the provider id. The manifest and the registration are written by hand
// in two different files, which is why the match is case-insensitive.
func TestByIntegration(t *testing.T) {
	Register(ProviderInfo{
		Name: "byint-a", Integration: "ByIntAcme", DefaultModel: "m-a",
		Provider: &fakeProvider{reply: "a"},
	})

	// Exact, and in every casing the two hand-written files might disagree on.
	for _, in := range []string{"ByIntAcme", "byintacme", "BYINTACME", "bYiNtAcMe"} {
		p, ok := ByIntegration(in)
		if !ok {
			t.Errorf("ByIntegration(%q) not found", in)
			continue
		}
		if p.Name != "byint-a" || p.DefaultModel != "m-a" {
			t.Errorf("ByIntegration(%q) = %+v, want byint-a", in, p)
		}
	}

	// Unknown integration reports not-found with a zero value, rather than a
	// half-populated provider the caller might use anyway.
	p, ok := ByIntegration("no-such-integration")
	if ok {
		t.Errorf("unknown integration returned %+v", p)
	}
	if p.Name != "" || p.Provider != nil {
		t.Errorf("not-found should yield the zero ProviderInfo, got %+v", p)
	}

	// Whitespace is NOT trimmed — the integration name comes from a manifest,
	// not from a person typing, so a padded value is a manifest bug worth
	// surfacing rather than silently accepting.
	if _, ok := ByIntegration(" ByIntAcme "); ok {
		t.Error("ByIntegration should not trim; a padded manifest value is a bug")
	}

	// An empty integration must not match a provider that left it blank by
	// accident — guard the "everything matches nothing" case explicitly.
	if _, ok := ByIntegration(""); ok {
		t.Error(`ByIntegration("") should not match any registered provider`)
	}

	// Re-registering the same id updates it in place, so the integration lookup
	// follows the new value rather than resolving to a stale one.
	Register(ProviderInfo{
		Name: "byint-a", Integration: "ByIntRenamed", DefaultModel: "m-a2",
		Provider: &fakeProvider{reply: "a2"},
	})
	if _, ok := ByIntegration("ByIntAcme"); ok {
		t.Error("the old integration still resolves after a re-registration")
	}
	if p, ok := ByIntegration("byintrenamed"); !ok || p.DefaultModel != "m-a2" {
		t.Errorf("renamed integration = %+v, ok=%v", p, ok)
	}
}

// RegisteredNames returns provider ids, sorted, for stable test/debug output.
func RegisteredNames() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := append([]string(nil), order...)
	sort.Strings(out)
	return out
}
