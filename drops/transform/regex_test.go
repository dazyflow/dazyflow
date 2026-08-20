// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runRegex(t *testing.T, in any, params map[string]any) core.Result {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	res, err := executeRegex(t.Context(), core.Job{
		ID:     "test",
		Params: params,
		Input:  map[string]core.Ref{"in": {Inline: in}},
	}, nil)
	if err != nil {
		t.Fatalf("executeRegex returned error: %v", err)
	}
	return res
}

func regexOut(t *testing.T, res core.Result) core.Ref {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	return res.Output["out"]
}

func TestRegex_ExtractAllMatches(t *testing.T) {
	res := runRegex(t, "a1 b22 c333", map[string]any{"pattern": "[0-9]+", "mode": "extract"})
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("rows = %#v, want 3", res.Output["rows"].Inline)
	}
	if rows[0]["match"] != "1" || rows[2]["match"] != "333" {
		t.Errorf("matches = %+v", rows)
	}
	if regexOut(t, res).Inline != "1" {
		t.Errorf("out = %v, want first match 1", regexOut(t, res).Inline)
	}
}

func TestRegex_ExtractNamedGroups(t *testing.T) {
	res := runRegex(t, "2026-07 and 1999-12", map[string]any{
		"pattern": `(?P<year>\d{4})-(?P<month>\d{2})`, "mode": "extract",
	})
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["year"] != "2026" || rows[0]["month"] != "07" {
		t.Errorf("row0 = %+v, want year=2026 month=07", rows[0])
	}
	if rows[0]["match"] != "2026-07" {
		t.Errorf("row0 match = %v, want 2026-07", rows[0]["match"])
	}
	// Headers include the group names in order.
	h := res.Output["rows"].Headers
	if len(h) != 3 || h[0] != "match" || h[1] != "year" || h[2] != "month" {
		t.Errorf("headers = %v, want [match year month]", h)
	}
}

func TestRegex_ExtractUnnamedGroupsByPosition(t *testing.T) {
	res := runRegex(t, "key=value", map[string]any{"pattern": `(\w+)=(\w+)`, "mode": "extract"})
	rows := res.Output["rows"].Inline.([]map[string]any)
	if rows[0]["1"] != "key" || rows[0]["2"] != "value" {
		t.Errorf("row0 = %+v, want 1=key 2=value", rows[0])
	}
}

func TestRegex_ExtractNoMatch(t *testing.T) {
	res := runRegex(t, "no digits here", map[string]any{"pattern": "[0-9]+", "mode": "extract"})
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
	if regexOut(t, res).Inline != "" {
		t.Errorf("out = %v, want empty string", regexOut(t, res).Inline)
	}
}

func TestRegex_ReplaceWithGroupRef(t *testing.T) {
	res := runRegex(t, "John Smith", map[string]any{
		"pattern": `(\w+)\s+(\w+)`, "mode": "replace", "replacement": "$2, $1",
	})
	out := regexOut(t, res)
	if out.Inline != "Smith, John" {
		t.Errorf("out = %v, want \"Smith, John\"", out.Inline)
	}
	if out.MIME != "text/plain" {
		t.Errorf("MIME = %q, want text/plain", out.MIME)
	}
}

func TestRegex_ReplaceCollapseWhitespace(t *testing.T) {
	res := runRegex(t, "a   b\t c", map[string]any{"pattern": `\s+`, "mode": "replace", "replacement": "-"})
	if got := regexOut(t, res).Inline; got != "a-b-c" {
		t.Errorf("out = %v, want a-b-c", got)
	}
}

func TestRegex_Split(t *testing.T) {
	res := runRegex(t, "a, b ,c,  d", map[string]any{"pattern": `\s*,\s*`, "mode": "split"})
	out := regexOut(t, res)
	parts, ok := out.Inline.([]string)
	if !ok || len(parts) != 4 {
		t.Fatalf("parts = %#v, want 4 strings", out.Inline)
	}
	if parts[0] != "a" || parts[3] != "d" {
		t.Errorf("parts = %v", parts)
	}
	if out.MIME != "application/json" {
		t.Errorf("MIME = %q, want application/json", out.MIME)
	}
}

func TestRegex_MatchTrueFalse(t *testing.T) {
	yes := runRegex(t, "INV-42", map[string]any{"pattern": `^INV-\d+$`, "mode": "match"})
	if regexOut(t, yes).Inline != true {
		t.Errorf("match INV-42 = %v, want true", regexOut(t, yes).Inline)
	}
	if regexOut(t, yes).MIME != core.MIMEBool {
		t.Errorf("MIME = %q, want %q", regexOut(t, yes).MIME, core.MIMEBool)
	}
	no := runRegex(t, "PO-42", map[string]any{"pattern": `^INV-\d+$`, "mode": "match"})
	if regexOut(t, no).Inline != false {
		t.Errorf("match PO-42 = %v, want false", regexOut(t, no).Inline)
	}
}

func TestRegex_CaseInsensitiveInlineFlag(t *testing.T) {
	res := runRegex(t, "HELLO world", map[string]any{"pattern": `(?i)hello`, "mode": "match"})
	if regexOut(t, res).Inline != true {
		t.Errorf("(?i)hello vs HELLO = %v, want true", regexOut(t, res).Inline)
	}
}

func TestRegex_MissingPattern(t *testing.T) {
	res := runRegex(t, "x", map[string]any{"mode": "match"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status/code = %v/%v, want error/bad_param", res.Status, res.Error)
	}
}

func TestRegex_InvalidPattern(t *testing.T) {
	res := runRegex(t, "x", map[string]any{"pattern": "([unclosed", "mode": "match"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status/code = %v/%v, want error/bad_param", res.Status, res.Error)
	}
}

func TestRegex_UnknownMode(t *testing.T) {
	res := runRegex(t, "x", map[string]any{"pattern": "x", "mode": "frobnicate"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status/code = %v/%v, want error/bad_param", res.Status, res.Error)
	}
}

func TestRegex_NonStringInput(t *testing.T) {
	res := runRegex(t, 42, map[string]any{"pattern": "x", "mode": "match"})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}

// Inside a For each there is no upstream node to wire from — the item's
// fields arrive as ${item.…} in a step's own params. So the text has to be
// typeable, with a wired input still winning when there is one.
func TestRegex_TextParam(t *testing.T) {
	res, err := executeRegex(t.Context(), core.Job{
		ID: "test",
		Params: map[string]any{
			"pattern": `\+?[0-9][0-9 ()-]{6,}[0-9]`,
			"mode":    "extract",
			"text":    "Kund: Ida, tel 070-123 45 67",
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%v err=%v error=%+v", res.Status, err, res.Error)
	}
	if got := res.Output["out"].Inline; got != "070-123 45 67" {
		t.Errorf("out = %v, want the phone number", got)
	}
}

func TestRegex_WiredInputBeatsTextParam(t *testing.T) {
	res, _ := executeRegex(t.Context(), core.Job{
		ID:     "test",
		Params: map[string]any{"pattern": `[0-9]+`, "mode": "extract", "text": "typed 111"},
		Input:  map[string]core.Ref{"in": {Inline: "wired 222"}},
	}, nil)
	if got := res.Output["out"].Inline; got != "222" {
		t.Errorf("out = %v, want the wired text to win", got)
	}
}
