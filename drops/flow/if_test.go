// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestIf_RoutesByVerdict checks that each operator routes the A payload down
// the right port, exercising the operators most likely to be used as a filter.
func TestIf_RoutesByVerdict(t *testing.T) {
	for _, c := range []struct {
		name   string
		params map[string]any
		a      any
		want   string // "then" (Yes) or "else" (No)
	}{
		{"contains match", map[string]any{"op": "contains", "B": "urgent"}, "this is urgent", "then"},
		{"contains miss", map[string]any{"op": "contains", "B": "urgent"}, "all calm here", "else"},
		{"not_contains match", map[string]any{"op": "not_contains", "B": "spam"}, "a clean message", "then"},
		{"equals match", map[string]any{"op": "equals", "B": "active"}, "active", "then"},
		{"equals miss", map[string]any{"op": "equals", "B": "active"}, "paused", "else"},
		{"in_range match", map[string]any{"op": "in_range", "B": "[200,299]"}, 204.0, "then"},
		{"in_range miss", map[string]any{"op": "in_range", "B": "[200,299]"}, 404.0, "else"},
		{"one_of match", map[string]any{"op": "one_of", "B": "[200,201,204]"}, 201.0, "then"},
		{"default op is equals", map[string]any{"B": "x"}, "x", "then"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, _ := executeIf(t.Context(), core.Job{
				Params: c.params,
				Input:  map[string]core.Ref{"A": {Inline: c.a}},
			}, nil)
			if res.Status != core.StatusOK {
				t.Fatalf("status=%q (%+v)", res.Status, res.Error)
			}
			if _, ok := res.Output[c.want]; !ok {
				t.Errorf("got %v, want payload on %q", keys(res.Output), c.want)
			}
			other := "else"
			if c.want == "else" {
				other = "then"
			}
			if _, ok := res.Output[other]; ok {
				t.Errorf("payload also emitted on %q; must fire exactly one port", other)
			}
		})
	}
}

// TestIf_ForwardsPayloadWithMetadata confirms the A ref (MIME + inline) is
// forwarded intact, not just its inline value.
func TestIf_ForwardsPayloadWithMetadata(t *testing.T) {
	res, _ := executeIf(t.Context(), core.Job{
		Params: map[string]any{"op": "contains", "B": "hi"},
		Input:  map[string]core.Ref{"A": {MIME: "text/plain", Inline: "hi there"}},
	}, nil)
	out := res.Output["then"]
	if out.Inline != "hi there" || out.MIME != "text/plain" {
		t.Errorf("If lost payload metadata: %+v", out)
	}
}

// TestIf_TestsFieldButRoutesWholePayload checks that `field` scopes the test to
// a nested value while the entire A payload still routes.
func TestIf_TestsFieldButRoutesWholePayload(t *testing.T) {
	payload := map[string]any{"status": "active", "id": 42.0}
	res, _ := executeIf(t.Context(), core.Job{
		Params: map[string]any{"op": "equals", "field": "status", "B": "active"},
		Input:  map[string]core.Ref{"A": {MIME: "application/json", Inline: payload}},
	}, nil)
	out, ok := res.Output["then"]
	if !ok {
		t.Fatalf("expected Yes port; got %v", keys(res.Output))
	}
	if m, ok := out.Inline.(map[string]any); !ok || m["id"] != 42.0 {
		t.Errorf("routed payload should be the whole A object, got %+v", out.Inline)
	}
}

// TestContainsPreset routes via the fixed-op Contains drop: B is the substring,
// A is the text that flows on.
func TestContainsPreset(t *testing.T) {
	run := func(text, sub string) core.Result {
		res, _ := executeContains(t.Context(), core.Job{
			Params: map[string]any{"B": sub},
			Input:  map[string]core.Ref{"A": {MIME: "text/plain", Inline: text}},
		}, nil)
		return res
	}

	hit := run("this is urgent", "urgent")
	if _, ok := hit.Output["then"]; !ok {
		t.Errorf(`"urgent" in "this is urgent" should route to "then", got %v`, keys(hit.Output))
	}
	if out := hit.Output["then"]; out.Inline != "this is urgent" {
		t.Errorf("Contains should forward the text payload, got %v", out.Inline)
	}

	miss := run("all calm", "urgent")
	if _, ok := miss.Output["else"]; !ok {
		t.Errorf(`miss should route to "else", got %v`, keys(miss.Output))
	}
}

// TestIf_BadOperandIsError mirrors Compare: contains on a non-text value is an
// explicit error rather than a silent misroute.
func TestIf_BadOperandIsError(t *testing.T) {
	res, _ := executeIf(t.Context(), core.Job{
		Params: map[string]any{"op": "contains", "B": "x"},
		Input:  map[string]core.Ref{"A": {Inline: []any{1, 2, 3}}},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
}
