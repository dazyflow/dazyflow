// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"reflect"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/mcp"
)

// TestManifestsForSubsetAgreesWithFullMap is the contract that makes
// ManifestsForSubset safe to use in place of ManifestsForTenant.
//
// The subset read exists because the full one clones the whole built-in
// derivation for callers that only read a handful of entries. It is only a
// legitimate substitute if it returns exactly what the full map holds for the
// ids asked for — same manifests, and the same answer about which ids exist —
// across every catalog in the chain, since the subset takes a DIFFERENT route
// for built-ins (a point lookup into the registry's cache) than for the rest.
// A drift here would show a step's ports differently depending on where its
// drop came from.
func TestManifestsForSubsetAgreesWithFullMap(t *testing.T) {
	reg := NewRegistry()
	for _, id := range []string{"native-a", "native-b", "native-c"} {
		if err := reg.Register(NativeDrop{Manifest: validTestManifest(id), Execute: noopExecute}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	remote := NewRemoteCatalog()
	remote.nodes[remoteKey{tenant: "acme", id: "remote-mod"}] = &RemoteTransport{
		manifest: core.Manifest{ID: "remote-mod"},
	}
	r := &NodeResolver{Native: reg, Remote: remote, MCP: mcp.NewCatalog()}

	full := r.ManifestsForTenant("acme")

	cases := [][]string{
		{"native-a"},                         // built-in only: the fast path
		{"native-a", "native-b", "native-c"}, // several built-ins
		{"remote-mod"},                       // outside the built-in catalog: the fallback
		{"native-a", "remote-mod"},           // mixed, so both routes run in one call
		{"native-a", "native-a", "native-a"}, // repeats must not duplicate or diverge
		{"ghost"},                            // absent everywhere
		{"native-a", "ghost", "remote-mod"},  // present and absent together
		{},                                   // nothing asked for
	}
	for _, ids := range cases {
		got := r.ManifestsForSubset("acme", ids)

		want := map[string]core.Manifest{}
		for _, id := range ids {
			if m, ok := full[id]; ok {
				want[id] = m
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ManifestsForSubset(%v):\n got  %v\n want %v", ids, keysOf(got), keysOf(want))
			continue
		}
		// Equality of the map is not enough on its own: assert the manifests
		// themselves match the full map's, so a subset that returned an
		// UNDERIVED manifest (no passthrough pin, no list-port marks) cannot
		// pass by having the right keys.
		for id, m := range got {
			if !reflect.DeepEqual(m, full[id]) {
				t.Errorf("subset[%q] differs from the full map's manifest", id)
			}
		}
	}
}

// TestManifestsForSubsetDoesNotAliasTheRegistry: the fast path hands back
// entries from the registry's CACHED derivation, so a caller that writes to
// the returned map must not reach the cache every other caller reads.
func TestManifestsForSubsetDoesNotAliasTheRegistry(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(NativeDrop{Manifest: validTestManifest("m"), Execute: noopExecute}); err != nil {
		t.Fatalf("register: %v", err)
	}
	r := &NodeResolver{Native: reg}

	got := r.ManifestsForSubset("", []string{"m"})
	got["m"] = core.Manifest{ID: "vandalized"}
	delete(got, "nothing")

	if again := r.ManifestsForSubset("", []string{"m"}); again["m"].ID != "m" {
		t.Errorf("writing to a subset result changed the registry: got %q", again["m"].ID)
	}
	if full := r.ManifestsForTenant(""); full["m"].ID != "m" {
		t.Errorf("writing to a subset result changed the full map: got %q", full["m"].ID)
	}
}

func keysOf(m map[string]core.Manifest) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
