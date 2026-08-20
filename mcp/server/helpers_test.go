// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"testing"
)

// TestParseErrorEnvelope_Shapes covers the legacy-string, structured,
// and unparseable branches of parseErrorEnvelope.
func TestParseErrorEnvelope_Shapes(t *testing.T) {
	t.Run("legacy string", func(t *testing.T) {
		code, msg, doc, det := parseErrorEnvelope([]byte(`{"error":"plain message"}`))
		if msg != "plain message" || code != "" || doc != "" || det != nil {
			t.Errorf("got code=%q msg=%q doc=%q det=%v", code, msg, doc, det)
		}
	})
	t.Run("structured", func(t *testing.T) {
		body := `{"error":{"code":"flow_locked","message":"m","doc":"/d","details":[{"field":"f","issue":"i"},{"field":"g"}]}}`
		code, msg, doc, det := parseErrorEnvelope([]byte(body))
		if code != "flow_locked" || msg != "m" || doc != "/d" {
			t.Errorf("got code=%q msg=%q doc=%q", code, msg, doc)
		}
		if len(det) != 2 || det[0].Field != "f" || det[0].Issue != "i" || det[1].Field != "g" {
			t.Errorf("details = %+v", det)
		}
	})
	t.Run("unparseable", func(t *testing.T) {
		code, msg, doc, det := parseErrorEnvelope([]byte(`not json`))
		if code != "" || msg != "" || doc != "" || det != nil {
			t.Errorf("expected zero values, got code=%q msg=%q doc=%q det=%v", code, msg, doc, det)
		}
	})
	t.Run("error not string or object", func(t *testing.T) {
		// error is a number → neither switch arm fires, all zero values.
		code, msg, _, _ := parseErrorEnvelope([]byte(`{"error":42}`))
		if code != "" || msg != "" {
			t.Errorf("got code=%q msg=%q", code, msg)
		}
	})
}

// TestBuildQuery covers empty-skip, empty-result, and encoded output.
func TestBuildQuery(t *testing.T) {
	if got := buildQuery(map[string]string{"a": "", "b": ""}); got != "" {
		t.Errorf("all-empty = %q, want empty", got)
	}
	got := buildQuery(map[string]string{"a": "1", "b": "", "c": "x y"})
	// Keys are sorted by url.Values.Encode; empty b is skipped.
	if got != "?a=1&c=x+y" {
		t.Errorf("buildQuery = %q", got)
	}
}

// TestComposeFlowID and pathSegment cover the URL-encoding helpers.
func TestComposeFlowID_AndPathSegment(t *testing.T) {
	if got := composeFlowID("t", "ws", "id"); got != "t%2Fws%2Fid" {
		t.Errorf("composeFlowID = %q", got)
	}
	if got := pathSegment("a/b"); got != "a%2Fb" {
		t.Errorf("pathSegment = %q", got)
	}
}

// TestConnectionSlug covers the lower/trim/space-to-dash transform.
func TestConnectionSlug(t *testing.T) {
	cases := map[string]string{
		"  Email ":   "email",
		"My Service": "my-service",
		"ntfy":       "ntfy",
		"A B  C":     "a-b--c",
	}
	for in, want := range cases {
		if got := connectionSlug(in); got != want {
			t.Errorf("connectionSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRequiredFieldsMessage covers the 0/1/2/3+ field phrasings.
func TestRequiredFieldsMessage(t *testing.T) {
	cases := []struct {
		keys []string
		want string
	}{
		{nil, "required fields missing"},
		{[]string{"id"}, "id is required"},
		{[]string{"name", "value"}, "name and value are required"},
		{[]string{"run_id", "node_id", "decision"}, "run_id, node_id, and decision are required"},
	}
	for _, c := range cases {
		if got := requiredFieldsMessage(c.keys); got != c.want {
			t.Errorf("requiredFieldsMessage(%v) = %q, want %q", c.keys, got, c.want)
		}
	}
}

// TestStringField covers fallback, nil-map, wrong-type, and hit paths.
func TestStringField(t *testing.T) {
	if got := stringField(nil, "k", "fb"); got != "fb" {
		t.Errorf("nil map = %q", got)
	}
	if got := stringField(map[string]any{}, "k", "fb"); got != "fb" {
		t.Errorf("missing = %q", got)
	}
	if got := stringField(map[string]any{"k": 7}, "k", "fb"); got != "fb" {
		t.Errorf("wrong type = %q", got)
	}
	if got := stringField(map[string]any{"k": ""}, "k", "fb"); got != "fb" {
		t.Errorf("empty string = %q", got)
	}
	if got := stringField(map[string]any{"k": "v"}, "k", "fb"); got != "v" {
		t.Errorf("hit = %q", got)
	}
}

// TestIntField covers float64, int, json.Number, fallback, and nil-map.
func TestIntField(t *testing.T) {
	if got := intField(nil, "k", 5); got != 5 {
		t.Errorf("nil map = %d", got)
	}
	if got := intField(map[string]any{"k": float64(3)}, "k", 5); got != 3 {
		t.Errorf("float64 = %d", got)
	}
	if got := intField(map[string]any{"k": 4}, "k", 5); got != 4 {
		t.Errorf("int = %d", got)
	}
	if got := intField(map[string]any{"k": json.Number("9")}, "k", 5); got != 9 {
		t.Errorf("json.Number = %d", got)
	}
	if got := intField(map[string]any{"k": json.Number("nope")}, "k", 5); got != 5 {
		t.Errorf("bad json.Number = %d", got)
	}
	if got := intField(map[string]any{"k": "str"}, "k", 5); got != 5 {
		t.Errorf("wrong type = %d", got)
	}
}

// TestScoped covers explicit-args precedence and the missing-scope error.
func TestScoped(t *testing.T) {
	d := Defaults{Tenant: "dt", Workspace: "dw"}
	tn, ws, err := scoped(map[string]any{}, d)
	if err != nil || tn != "dt" || ws != "dw" {
		t.Errorf("defaults: tn=%q ws=%q err=%v", tn, ws, err)
	}
	tn, ws, err = scoped(map[string]any{"tenant": "x", "workspace": "y"}, d)
	if err != nil || tn != "x" || ws != "y" {
		t.Errorf("explicit: tn=%q ws=%q err=%v", tn, ws, err)
	}
	if _, _, err := scoped(map[string]any{}, Defaults{}); err == nil {
		t.Error("expected error when scope unresolved")
	}
}

// TestIsTerminal covers terminal/non-terminal for both the lowercase
// status field and the capitalized fallback.
func TestIsTerminal(t *testing.T) {
	cases := []struct {
		rec  map[string]any
		want bool
	}{
		{map[string]any{"status": "succeeded"}, true},
		{map[string]any{"status": "failed"}, true},
		{map[string]any{"status": "running"}, false},
		{map[string]any{"Status": "cancelled"}, true},
		{map[string]any{"Status": "queued"}, false},
		{map[string]any{}, false},
	}
	for _, c := range cases {
		if got := isTerminal(c.rec); got != c.want {
			t.Errorf("isTerminal(%v) = %v, want %v", c.rec, got, c.want)
		}
	}
}

// TestIdempotencyKey covers determinism and name/args separation, plus
// the context round-trip helpers.
func TestIdempotencyKey(t *testing.T) {
	k1 := idempotencyKeyFor("set_secret", json.RawMessage(`{"name":"A"}`))
	k2 := idempotencyKeyFor("set_secret", json.RawMessage(`{"name":"A"}`))
	if k1 != k2 || len(k1) != 32 {
		t.Errorf("nondeterministic or wrong length: %q %q", k1, k2)
	}
	if idempotencyKeyFor("a", json.RawMessage(`b`)) == idempotencyKeyFor("ab", json.RawMessage(``)) {
		t.Error("name/args separator collision")
	}

	ctx := withIdempotencyKey(context.Background(), "key1")
	if idempotencyKeyFromContext(ctx) != "key1" {
		t.Error("context round-trip failed")
	}
	// Empty key leaves the context untouched.
	if got := idempotencyKeyFromContext(withIdempotencyKey(context.Background(), "")); got != "" {
		t.Errorf("empty key stored = %q", got)
	}
}

// TestDecodeArgs covers empty input, valid object, and decode error.
func TestDecodeArgs(t *testing.T) {
	m, err := decodeArgs(nil)
	if err != nil || len(m) != 0 {
		t.Errorf("empty: m=%v err=%v", m, err)
	}
	m, err = decodeArgs(json.RawMessage(`{"a":1}`))
	if err != nil || m["a"].(float64) != 1 {
		t.Errorf("valid: m=%v err=%v", m, err)
	}
	if _, err := decodeArgs(json.RawMessage(`[1,2]`)); err == nil {
		t.Error("expected decode error for array")
	}
}

// TestTextResult_MarshalFailure covers the marshal-error branch of
// TextResult via a value json cannot encode (a channel).
func TestTextResult_MarshalFailure(t *testing.T) {
	res := TextResult(map[string]any{"bad": make(chan int)})
	if !res.IsError {
		t.Fatal("expected IsError for unmarshalable payload")
	}
	if res.Content[0].Text == "" {
		t.Error("expected marshal-failure message")
	}
}

// TestErrorResultOrErr_NonHTTP covers the pass-through of a non-HTTPError
// (transport-style) error and the nil-error case.
func TestErrorResultOrErr_NonHTTP(t *testing.T) {
	if res, err := errorResultOrErr(nil); err != nil || res.IsError {
		t.Errorf("nil error: res=%v err=%v", res, err)
	}
	plain := context.Canceled
	if _, err := errorResultOrErr(plain); err != plain {
		t.Errorf("non-HTTP error should pass through, got %v", err)
	}
}

// TestIdempotencyKeyFor_CanonicalizesArgs is the regression for a duplicated
// side effect on retry: an MCP host that re-serializes the arguments (key
// order, whitespace, number formatting held constant) must still land on the
// same idempotency key, or the gateway treats the retry as a new request.
func TestIdempotencyKeyFor_CanonicalizesArgs(t *testing.T) {
	same := []string{
		`{"flow":"a","tenant":"acme"}`,
		`{"tenant":"acme","flow":"a"}`,
		`{ "tenant" : "acme" ,  "flow" : "a" }`,
		"{\n  \"flow\": \"a\",\n  \"tenant\": \"acme\"\n}",
	}
	want := idempotencyKeyFor("run_flow", json.RawMessage(same[0]))
	for _, in := range same[1:] {
		if got := idempotencyKeyFor("run_flow", json.RawMessage(in)); got != want {
			t.Errorf("key for %s = %s, want %s (same call, retried)", in, got, want)
		}
	}

	// Genuinely different args must NOT collide — a false match would
	// silently suppress a distinct action.
	for _, in := range []string{
		`{"flow":"b","tenant":"acme"}`,
		`{"flow":"a","tenant":"acme","dry_run":true}`,
		`{"flow":"a"}`,
	} {
		if got := idempotencyKeyFor("run_flow", json.RawMessage(in)); got == want {
			t.Errorf("key for %s collided with the baseline", in)
		}
	}
	// Different tool, same args, must differ.
	if idempotencyKeyFor("delete_flow", json.RawMessage(same[0])) == want {
		t.Error("tool name is not namespacing the key")
	}
}

// TestIdempotencyKeyFor_PreservesNumericPrecision pins the UseNumber choice:
// two int64 arguments that differ only beyond float64's 53-bit mantissa must
// still produce different keys.
func TestIdempotencyKeyFor_PreservesNumericPrecision(t *testing.T) {
	a := idempotencyKeyFor("t", json.RawMessage(`{"n":9007199254740993}`))
	b := idempotencyKeyFor("t", json.RawMessage(`{"n":9007199254740992}`))
	if a == b {
		t.Error("large ints collapsed onto one key — a distinct call would be wrongly deduped")
	}
}

// TestIdempotencyKeyFor_InvalidJSONIsStable covers the fallback: args that
// aren't valid JSON still hash deterministically rather than panicking.
func TestIdempotencyKeyFor_InvalidJSONIsStable(t *testing.T) {
	const bad = `{"flow":`
	if idempotencyKeyFor("t", json.RawMessage(bad)) != idempotencyKeyFor("t", json.RawMessage(bad)) {
		t.Error("invalid JSON should still hash deterministically")
	}
	if idempotencyKeyFor("t", nil) != idempotencyKeyFor("t", json.RawMessage("")) {
		t.Error("nil and empty args should agree")
	}
}
