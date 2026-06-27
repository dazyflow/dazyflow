package pollstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

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
