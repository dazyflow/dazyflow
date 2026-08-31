// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package usecases

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
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

// awaitStableUTCDay waits out the last moments before UTC midnight. A test
// that computes a date in Go and compares it against a date the drop computes
// from its own clock is otherwise flaky for one second a day — rare enough to
// pass review and land in CI, common enough to fail eventually.
func awaitStableUTCDay(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	nextDay := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	if margin := nextDay.Sub(now); margin < 5*time.Second {
		t.Logf("waiting %s for the UTC day to roll over before comparing dates", margin)
		time.Sleep(margin + 100*time.Millisecond)
	}
}

// nowPlus is an RFC3339 timestamp h hours from now — the scenarios filter on
// a window relative to the run, so fixed dates would rot.
func nowPlus(t *testing.T, h int) string {
	t.Helper()
	return time.Now().UTC().Add(time.Duration(h) * time.Hour).Format(time.RFC3339)
}

// Use case 3: "email me a summary every Monday" — the week filter and the
// per-salesperson totals.
func TestDigestFormulas(t *testing.T) {
	// The two dates are relative to the run because the filter is: one day
	// inside the 168h window, one month outside it. A literal date here is a
	// test with an expiry date — this pair was written as 2026-08-19, one day
	// old against a 7-day window, and started failing six days later when the
	// window moved past it. That is what nowPlus is for.
	out := runDrop(t, "compute_rows", map[string]any{
		"compute": map[string]any{"amount": "double(row.amount)"},
		"filter":  "timestamp(row.date) > now - duration('168h')",
	}, map[string]core.Ref{"rows": jsonRef([]any{
		map[string]any{"date": nowPlus(t, -24), "salesperson": "Ida", "amount": "1200"},
		map[string]any{"date": nowPlus(t, -24*30), "salesperson": "Nils", "amount": "5000"},
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

// Use case 14: "does what we were paid match what we invoiced?" — one formula
// has to name all three kinds of mismatch off a full outer join, where either
// side may be missing. The lazy ternary is what keeps double(null) from
// blowing up on the rows where the other side is absent.
func TestReconciliationClassifier(t *testing.T) {
	const classify = "row.paid_amount == null ? 'invoiced but not paid' : " +
		"(row.amount == null ? 'paid but never invoiced' : " +
		"(double(row.amount) != double(row.paid_amount) ? 'amount differs' : ''))"

	out := runDrop(t, "compute_rows", map[string]any{
		"compute": map[string]any{"problem": classify},
	}, map[string]core.Ref{"rows": jsonRef([]any{
		map[string]any{"invoice_no": "A1", "amount": 1000, "paid_amount": 1000},
		map[string]any{"invoice_no": "A2", "amount": 500, "paid_amount": nil},
		map[string]any{"invoice_no": "A3", "amount": nil, "paid_amount": 250},
		map[string]any{"invoice_no": "A4", "amount": 300, "paid_amount": 299},
	})})
	rows := rowsOf(t, out["rows"])
	want := []string{"", "invoiced but not paid", "paid but never invoiced", "amount differs"}
	for i, w := range want {
		if rows[i]["problem"] != w {
			t.Errorf("row %d: problem = %q, want %q", i, rows[i]["problem"], w)
		}
	}
}

// Use case 29: each customer's own rows, collected into their own statement.
// The grouping has to carry the lines along, not just the totals — that list
// is what the template walks.
func TestPerCustomerStatement(t *testing.T) {
	grouped := runDrop(t, "group_aggregate", map[string]any{
		"by": []any{"customer", "email"},
		"aggregate": map[string]any{
			"total": map[string]any{"op": "sum", "column": "amount"},
			"lines": map[string]any{"op": "collect", "column": "description"},
			"count": map[string]any{"op": "count"},
		},
	}, map[string]core.Ref{"rows": jsonRef([]any{
		map[string]any{"customer": "Acme", "email": "a@x.se", "description": "Klippning", "amount": 450},
		map[string]any{"customer": "Acme", "email": "a@x.se", "description": "Färg", "amount": 900},
		map[string]any{"customer": "Bolaget", "email": "b@x.se", "description": "Konsultation", "amount": 1200},
	})})
	rows := rowsOf(t, grouped["rows"])
	if len(rows) != 2 {
		t.Fatalf("groups = %v, want one per customer", rows)
	}
	lines, _ := rows[0]["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("Acme's lines = %v, want both charges", rows[0]["lines"])
	}

	// That group is what a loop hands the template as ${item.} — see
	// engine.TestItemWholeValue_KeepsStructure for the handover itself.
	out := runDrop(t, "render_template", map[string]any{
		"template": "<p>Hej {{.customer}},</p><ul>{{range .lines}}<li>{{.}}</li>{{end}}</ul><p>Total: {{.total}} kr</p>",
	}, map[string]core.Ref{"data": jsonRef(rows[0])})
	html, _ := out["html"].Inline.(string)
	for _, want := range []string{"Hej Acme", "<li>Klippning</li>", "<li>Färg</li>", "1350"} {
		if !strings.Contains(html, want) {
			t.Errorf("statement is missing %q:\n%s", want, html)
		}
	}
}

// Use cases 24 and 27 lean on the string helpers: a title trimmed out of a
// Slack message, and addresses tidied before they're deduplicated. Without
// them neither is expressible in a formula at all.
func TestStringHelpersInScenarios(t *testing.T) {
	out := runDrop(t, "compute_rows", map[string]any{
		"compute": map[string]any{
			"email": "row.email.trim().lowerAscii()",
			"name":  "row.name.trim()",
		},
		"filter": "row.email.trim() != '' && row.email.contains('@')",
	}, map[string]core.Ref{"rows": jsonRef([]any{
		map[string]any{"name": " Ida ", "email": " IDA@Example.SE "},
		map[string]any{"name": "Tom", "email": ""},
		map[string]any{"name": "Nils", "email": "not-an-email"},
	})})
	rows := rowsOf(t, out["rows"])
	if len(rows) != 1 || rows[0]["email"] != "ida@example.se" || rows[0]["name"] != "Ida" {
		t.Fatalf("cleaned rows = %v", rows)
	}

	long := runDrop(t, "expression", map[string]any{
		"expr": "input.size() > 60 ? input.substring(0, 60).trim() + '…' : input.trim()",
	}, map[string]core.Ref{"in": {MIME: "text/plain",
		Inline: " printer on floor 2 is jammed again and nobody can print the delivery notes"}})
	title, _ := long["out"].Inline.(string)
	if !strings.HasSuffix(title, "…") || len([]rune(title)) > 61 {
		t.Errorf("title = %q, want it trimmed to 60 characters plus an ellipsis", title)
	}
}

// Use case 31: only tomorrow, and only when tomorrow is actually bad.
func TestTomorrowsWeatherFilter(t *testing.T) {
	const filter = "row.date == string(now + duration('24h')).substring(0, 10) && " +
		"(row.temp_min < 0.0 || row.conditions.lowerAscii().contains('rain'))"
	// The filter reads its own clock inside the drop, so the test's idea of
	// "tomorrow" and the drop's must not straddle a UTC midnight.
	awaitStableUTCDay(t)
	today := time.Now().UTC().Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")

	out := runDrop(t, "compute_rows", map[string]any{"filter": filter},
		map[string]core.Ref{"rows": jsonRef([]any{
			map[string]any{"date": today, "temp_min": -5.0, "conditions": "Snow"},
			map[string]any{"date": tomorrow, "temp_min": 12.0, "conditions": "Clear"},
			map[string]any{"date": tomorrow, "temp_min": -3.0, "conditions": "Clear"},
			map[string]any{"date": tomorrow, "temp_min": 8.0, "conditions": "Rain"},
		})})
	rows := rowsOf(t, out["rows"])
	if len(rows) != 2 {
		t.Fatalf("kept %d row(s), want tomorrow's frost and rain only: %v", len(rows), rows)
	}
	for _, r := range rows {
		if r["date"] != tomorrow {
			t.Errorf("kept a row for %v", r["date"])
		}
	}
}
