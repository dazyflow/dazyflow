package llm

import (
	"testing"
)

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
