// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package pollstate

import (
	"context"
	"errors"
	"testing"
	"time"

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

// errStore returns a Reader/Writer pair whose calls fail, to exercise the
// best-effort error paths in Report and Read.
func errStore(raw string, rerr, werr error) (Reader, Writer) {
	r := func(_ context.Context, _, _ string) (string, error) {
		return raw, rerr
	}
	w := func(_ context.Context, _, _, _ string) error {
		return werr
	}
	return r, w
}

func TestReportWriterErrorIsBestEffort(t *testing.T) {
	r, w := errStore("", nil, errors.New("write failed"))
	SetStore(r, w)
	t.Cleanup(func() { SetStore(nil, nil) })

	// A failed write must not panic and must leave the flow's outcome alone.
	Report(context.Background(), core.Job{Tenant: "t", GraphID: "g"}, true)
}

func TestReadVariants(t *testing.T) {
	tests := []struct {
		name      string
		tenant    string
		graphID   string
		raw       string
		readErr   error
		wantNil   bool
		wantEmpty bool
	}{
		{name: "no tenant", tenant: "", graphID: "g", wantNil: true},
		{name: "no graph", tenant: "t", graphID: "", wantNil: true},
		{name: "reader error", tenant: "t", graphID: "g", readErr: errors.New("boom"), wantNil: true},
		{name: "empty raw", tenant: "t", graphID: "g", raw: "", wantNil: true},
		{name: "bad json", tenant: "t", graphID: "g", raw: "{not json", wantNil: true},
		{name: "valid empty marker", tenant: "t", graphID: "g", raw: `{"empty":true,"at":"2026-06-26T00:00:00Z"}`, wantEmpty: true},
		{name: "valid active marker", tenant: "t", graphID: "g", raw: `{"empty":false,"at":"2026-06-26T00:00:00Z"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, w := errStore(tc.raw, tc.readErr, nil)
			SetStore(r, w)
			t.Cleanup(func() { SetStore(nil, nil) })

			m := Read(context.Background(), tc.tenant, tc.graphID)
			if tc.wantNil {
				if m != nil {
					t.Fatalf("expected nil marker, got %+v", m)
				}
				return
			}
			if m == nil {
				t.Fatal("expected non-nil marker")
			}
			if m.Empty != tc.wantEmpty {
				t.Fatalf("Empty = %v, want %v", m.Empty, tc.wantEmpty)
			}
		})
	}
}

func TestReadNilReader(t *testing.T) {
	SetStore(nil, nil)
	if m := Read(context.Background(), "t", "g"); m != nil {
		t.Fatalf("expected nil marker when unwired, got %+v", m)
	}
}

func TestParseAtVariants(t *testing.T) {
	tests := []struct {
		name     string
		marker   *Marker
		wantZero bool
	}{
		{name: "nil receiver", marker: nil, wantZero: true},
		{name: "empty timestamp", marker: &Marker{}, wantZero: true},
		{name: "malformed timestamp", marker: &Marker{At: "not-a-time"}, wantZero: true},
		{name: "valid timestamp", marker: &Marker{At: "2026-06-26T12:30:00Z"}, wantZero: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.marker.ParseAt()
			if got.IsZero() != tc.wantZero {
				t.Fatalf("ParseAt().IsZero() = %v, want %v (got %v)", got.IsZero(), tc.wantZero, got)
			}
			if !tc.wantZero {
				want, _ := time.Parse(time.RFC3339, tc.marker.At)
				if !got.Equal(want) {
					t.Fatalf("ParseAt() = %v, want %v", got, want)
				}
			}
		})
	}
}
