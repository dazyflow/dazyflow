// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package usecases

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// The graph test proves a use case COMPOSES. This one proves the formulas
// inside it actually do what /SCENARIOS.md claims: the CEL filters, the
// grouping, the column mapping. These are the parts a non-technical user
// cannot debug, and the parts that pass a structural check while quietly
// producing the wrong rows — the null-vs-missing filter below was exactly
// that: it validated fine and silently dropped every row.
func runDrop(t *testing.T, module string, params map[string]any, in map[string]core.Ref) map[string]core.Ref {
	t.Helper()
	tr, ok := engine.Default.Get(module)
	if !ok {
		t.Fatalf("no such module %q", module)
	}
	res, err := tr.Execute(context.Background(), core.Job{ID: "t", NodeID: module, Params: params, Input: in}, nil)
	if err != nil {
		t.Fatalf("%s: %v", module, err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("%s: %+v", module, res.Error)
	}
	return res.Output
}

// rowsOf normalises a rows output to []map[string]any for comparison.
func rowsOf(t *testing.T, r core.Ref) []map[string]any {
	t.Helper()
	b, err := json.Marshal(r.Inline)
	if err != nil {
		t.Fatalf("output is not JSON-serialisable: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("output is not a row list: %v", err)
	}
	return out
}

func jsonRef(v any) core.Ref { return core.Ref{MIME: "application/json", Inline: v} }

// nowPlus is an RFC3339 timestamp h hours from now — the scenarios filter on
// a window relative to the run, so fixed dates would rot.
func nowPlus(t *testing.T, h int) string {
	t.Helper()
	return time.Now().UTC().Add(time.Duration(h) * time.Hour).Format(time.RFC3339)
}

// Use case 3: "email me a summary every Monday" — the week filter and the
// per-salesperson totals.
func TestDigestFormulas(t *testing.T) {
	out := runDrop(t, "compute_rows", map[string]any{
		"compute": map[string]any{"amount": "double(row.amount)"},
		"filter":  "timestamp(row.date) > now - duration('168h')",
	}, map[string]core.Ref{"rows": jsonRef([]any{
		map[string]any{"date": "2026-08-19T10:00:00Z", "salesperson": "Ida", "amount": "1200"},
		map[string]any{"date": "2020-01-02T10:00:00Z", "salesperson": "Nils", "amount": "5000"},
	})})
	rows := rowsOf(t, out["rows"])
	if len(rows) != 1 || rows[0]["salesperson"] != "Ida" {
		t.Fatalf("week filter kept the wrong rows: %v", rows)
	}

	out = runDrop(t, "group_aggregate", map[string]any{
		"by": []any{"salesperson"},
		"aggregate": map[string]any{
			"orders":  map[string]any{"op": "count"},
			"revenue": map[string]any{"op": "sum", "column": "amount"},
		},
	}, map[string]core.Ref{"rows": jsonRef([]any{
		map[string]any{"salesperson": "Ida", "amount": 1200},
		map[string]any{"salesperson": "Ida", "amount": 800},
		map[string]any{"salesperson": "Nils", "amount": 300},
	})})
	rows = rowsOf(t, out["rows"])
	if len(rows) != 2 || rows[0]["revenue"] != float64(2000) || rows[0]["orders"] != float64(2) {
		t.Fatalf("totals are wrong: %v", rows)
	}
}

// Use case 7: "text my customers the day before" — only tomorrow's bookings
// that actually have someone to notify.
func TestTomorrowsBookingsFilter(t *testing.T) {
	out := runDrop(t, "compute_rows", map[string]any{
		"compute": map[string]any{
			"who":  "size(row.attendees) > 0 ? row.attendees[0] : ''",
			"when": "row.start",
		},
		"filter": "timestamp(row.start) > now && timestamp(row.start) < now + duration('48h') && size(row.attendees) > 0",
	}, map[string]core.Ref{"rows": jsonRef([]any{
		map[string]any{"id": "e1", "summary": "Klippning", "start": nowPlus(t, 20), "attendees": []any{"kund@example.com"}},
		map[string]any{"id": "e2", "summary": "Internt", "start": nowPlus(t, 20), "attendees": []any{}},
		map[string]any{"id": "e3", "summary": "Nästa vecka", "start": nowPlus(t, 24*9), "attendees": []any{"sen@example.com"}},
	})})
	rows := rowsOf(t, out["rows"])
	if len(rows) != 1 || rows[0]["id"] != "e1" || rows[0]["who"] != "kund@example.com" {
		t.Fatalf("wrong bookings picked up: %v", rows)
	}
}

// Use case 10: "keep two systems in step" — the join that decides which rows
// are new. The anti join answers it directly; the left-join-plus-null-test
// that came before is the trap this replaced, and the second half pins why:
// join_rows fills unmatched right-hand columns with NULL rather than leaving
// them absent, so the intuitive has() test drops every row and the sync
// silently writes nothing.
func TestNotYetSyncedFilter(t *testing.T) {
	left := jsonRef([]any{map[string]any{"email": "a@x.se"}, map[string]any{"email": "b@x.se"}})
	right := jsonRef([]any{map[string]any{"email": "a@x.se", "synced_at": "2026-08-01"}})

	anti := runDrop(t, "join_rows", map[string]any{
		"on": map[string]any{"email": "email"}, "kind": "anti",
	}, map[string]core.Ref{"left_rows": left, "right_rows": right})
	rows := rowsOf(t, anti["rows"])
	if len(rows) != 1 || rows[0]["email"] != "b@x.se" {
		t.Fatalf("anti join = %v, want only the unsynced row", rows)
	}
	if _, leaked := rows[0]["synced_at"]; leaked {
		t.Errorf("anti join leaked a right-side column: %v", rows[0])
	}

	// Why the graph doesn't do it the other way: after a left join the
	// unmatched column is present-and-null, so has() is true for it.
	joined := runDrop(t, "join_rows", map[string]any{
		"on": map[string]any{"email": "email"}, "kind": "left",
	}, map[string]core.Ref{"left_rows": left, "right_rows": right})
	out := runDrop(t, "compute_rows", map[string]any{
		"filter": "!has(row.synced_at) || row.synced_at == ''",
	}, map[string]core.Ref{"rows": joined["rows"]})
	if got := rowsOf(t, out["rows"]); len(got) != 0 {
		t.Fatalf("has() now works — simplify the note in /SCENARIOS.md: %v", got)
	}
}

// Use cases 4 and 7 build their log rows with map_rows, not a formula,
// because expression cannot emit objects (see /SCENARIOS.md, Findings).
// map_rows also accepts a single object as one row, which is what makes the
// "log this payment" shape work at all.
func TestLogRowShaping(t *testing.T) {
	out := runDrop(t, "map_rows", map[string]any{
		"select": []any{"id", "receipt_email", "amount", "currency", "description"},
		"rename": map[string]any{"id": "payment_id", "receipt_email": "email"},
	}, map[string]core.Ref{"rows": jsonRef(map[string]any{
		"id": "pi_1", "receipt_email": "kund@example.com", "amount": 49900,
		"currency": "sek", "description": "Order 1041", "livemode": true,
	})})
	rows := rowsOf(t, out["rows"])
	if len(rows) != 1 || rows[0]["payment_id"] != "pi_1" || rows[0]["email"] != "kund@example.com" {
		t.Fatalf("payment did not shape into one log row: %v", rows)
	}
	if _, leaked := rows[0]["livemode"]; leaked {
		t.Fatalf("unselected column leaked into the log row: %v", rows[0])
	}
}
