// SPDX-FileCopyrightText: 2026 Angels' Ware
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

// ----- the Replacements table ---------------------------------------
//
// Where this came from: an author wrote the pattern "(Clouds)|(Rain)" and tried
// "(?1Molnigt)(?2Regn)" as the replacement — Boost/PCRE conditional-replacement
// syntax, which Go's RE2 template has no notion of, so it came out literally.
// The thing being asked for is a lookup, and a table says it directly.

func TestRegex_ReplacementsTableTranslatesEachMatch(t *testing.T) {
	res := runRegex(t, "Clouds today, Rain tomorrow", map[string]any{
		"mode": "replace",
		// No pattern: the words to look for are the table's own keys.
		"replacements": map[string]any{"Clouds": "Molnigt", "Rain": "Regn"},
	})
	if got := regexOut(t, res).Inline; got != "Molnigt today, Regn tomorrow" {
		t.Errorf("out = %v, want \"Molnigt today, Regn tomorrow\"", got)
	}
}

func TestRegex_ReplacementsLeaveUnlistedMatchesAlone(t *testing.T) {
	// The author's own pattern matches more than the table mentions. The table
	// lists what to change; everything else is not ours to touch.
	res := runRegex(t, "Clouds and Fog", map[string]any{
		"pattern": `Clouds|Fog`, "mode": "replace",
		"replacements": map[string]any{"Clouds": "Molnigt"},
	})
	if got := regexOut(t, res).Inline; got != "Molnigt and Fog" {
		t.Errorf("out = %v, want \"Molnigt and Fog\"", got)
	}
}

func TestRegex_ReplacementsFollowTheirOwnPattern(t *testing.T) {
	// (?i) in the pattern decides what counts as a match; the table is then
	// consulted case-insensitively, since one row unambiguously covers it.
	res := runRegex(t, "CLOUDS then clouds", map[string]any{
		"pattern": `(?i)clouds`, "mode": "replace",
		"replacements": map[string]any{"Clouds": "Molnigt"},
	})
	if got := regexOut(t, res).Inline; got != "Molnigt then Molnigt" {
		t.Errorf("out = %v, want both replaced: %v", got, got)
	}
}

func TestRegex_ReplacementsAmbiguousCaseIsLeftAlone(t *testing.T) {
	// Two rows differing only in case: neither is guessed at, and the exact
	// one still wins for its own spelling.
	res := runRegex(t, "clouds CLOUDS Clouds", map[string]any{
		"pattern": `(?i)clouds`, "mode": "replace",
		"replacements": map[string]any{"Clouds": "Molnigt", "clouds": "molnigt"},
	})
	if got := regexOut(t, res).Inline; got != "molnigt CLOUDS Molnigt" {
		t.Errorf("out = %v, want the exact spellings replaced and CLOUDS untouched", got)
	}
}

func TestRegex_ReplacementsLongestKeyWins(t *testing.T) {
	// RE2 alternation is leftmost-first, so a shorter key listed first would
	// otherwise eat the start of a longer phrase and leave the rest behind.
	res := runRegex(t, "Rain shower later", map[string]any{
		"mode":         "replace",
		"replacements": map[string]any{"Rain": "Regn", "Rain shower": "Regnskur"},
	})
	if got := regexOut(t, res).Inline; got != "Regnskur later" {
		t.Errorf("out = %v, want \"Regnskur later\"", got)
	}
}

func TestRegex_ReplacementsQuoteTheirKeys(t *testing.T) {
	// A key is a word to find, not an expression: punctuation is literal.
	res := runRegex(t, "cost is 5 (approx.)", map[string]any{
		"mode":         "replace",
		"replacements": map[string]any{"(approx.)": "(cirka)"},
	})
	if got := regexOut(t, res).Inline; got != "cost is 5 (cirka)" {
		t.Errorf("out = %v, want the parens matched literally", got)
	}
}

func TestRegex_ReplacementsIgnoreBlankKeys(t *testing.T) {
	// A half-typed row in the editor must not compile into a pattern that
	// matches the empty string everywhere.
	res := runRegex(t, "Clouds", map[string]any{
		"mode":         "replace",
		"replacements": map[string]any{"": "x", "Clouds": "Molnigt"},
	})
	if got := regexOut(t, res).Inline; got != "Molnigt" {
		t.Errorf("out = %v, want Molnigt", got)
	}
}

func TestRegex_ReplacementsTableWinsOverReplacement(t *testing.T) {
	// Documented precedence: with a table, the single replacement string is not
	// consulted at all (it has no per-match answer).
	res := runRegex(t, "Clouds", map[string]any{
		"mode": "replace", "replacement": "IGNORED",
		"replacements": map[string]any{"Clouds": "Molnigt"},
	})
	if got := regexOut(t, res).Inline; got != "Molnigt" {
		t.Errorf("out = %v, want Molnigt", got)
	}
}

func TestRegex_ReplacementsBadShapeIsAnError(t *testing.T) {
	for _, v := range []any{map[string]any{"Clouds": 42}, "Clouds=Molnigt", []any{"Clouds"}} {
		res := runRegex(t, "Clouds", map[string]any{"mode": "replace", "replacements": v})
		if res.Status != core.StatusError || res.Error.Code != "bad_param" {
			t.Errorf("replacements=%v: status/code = %v/%v, want error/bad_param", v, res.Status, res.Error)
		}
	}
}

func TestRegex_NoPatternAndNoTableIsAnError(t *testing.T) {
	// The table is the only thing that can stand in for a pattern, and only in
	// replace mode.
	for _, params := range []map[string]any{
		{"mode": "replace"},
		{"mode": "extract", "replacements": map[string]any{"Clouds": "Molnigt"}},
	} {
		res := runRegex(t, "Clouds", params)
		if res.Status != core.StatusError || res.Error.Code != "bad_param" {
			t.Errorf("params=%v: status/code = %v/%v, want error/bad_param", params, res.Status, res.Error)
		}
	}
}
