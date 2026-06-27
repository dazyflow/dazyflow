// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package pollstate

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// memStore is an in-memory Reader/Writer pair for tests.
func memStore() (Reader, Writer, map[string]string) {
	m := map[string]string{}
	r := func(_ context.Context, tenant, name string) (string, error) {
		return m[tenant+"/"+name], nil
	}
	w := func(_ context.Context, tenant, name, value string) error {
		m[tenant+"/"+name] = value
		return nil
	}
	return r, w, m
}

func TestReportRoundTrip(t *testing.T) {
	r, w, _ := memStore()
	SetStore(r, w)
	t.Cleanup(func() { SetStore(nil, nil) })

	job := core.Job{Tenant: "t", GraphID: "g1"}
	Report(context.Background(), job, false) // empty fire

	m := Read(context.Background(), "t", "g1")
	if m == nil || !m.Empty {
		t.Fatalf("expected empty marker, got %+v", m)
	}
	if m.ParseAt().IsZero() {
		t.Fatal("marker timestamp should parse")
	}

	Report(context.Background(), job, true) // active fire
	m = Read(context.Background(), "t", "g1")
	if m == nil || m.Empty {
		t.Fatalf("expected active marker, got %+v", m)
	}
}

func TestReportNoopWhenUnwired(t *testing.T) {
	SetStore(nil, nil)
	// Must not panic and Read returns nil.
	Report(context.Background(), core.Job{Tenant: "t", GraphID: "g"}, false)
	if m := Read(context.Background(), "t", "g"); m != nil {
		t.Fatalf("expected nil marker when unwired, got %+v", m)
	}
}

func TestReportNoopWithoutIdentity(t *testing.T) {
	r, w, store := memStore()
	SetStore(r, w)
	t.Cleanup(func() { SetStore(nil, nil) })

	Report(context.Background(), core.Job{GraphID: "g"}, false) // no tenant
	Report(context.Background(), core.Job{Tenant: "t"}, false)  // no graph
	if len(store) != 0 {
		t.Fatalf("expected no writes without tenant+graph, got %v", store)
	}
}

func TestNameStable(t *testing.T) {
	if Name("abc") != "pollstate.abc" {
		t.Fatalf("unexpected name %q", Name("abc"))
	}
}
