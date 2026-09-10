// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"math"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func runNumber(t *testing.T, p map[string]any, in any, language string) core.Result {
	t.Helper()
	job := core.Job{ID: "t", Params: p, Language: language}
	if in != nil {
		job.Input = map[string]core.Ref{"in": {Inline: in}}
	}
	res, err := executeFormatNumber(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("executeFormatNumber: %v", err)
	}
	return res
}

func numberText(t *testing.T, p map[string]any, in any, language string) string {
	t.Helper()
	res := runNumber(t, p, in, language)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	s, _ := res.Output["out"].Inline.(string)
	return s
}

// The whole reason this step exists: the same number, written for two readers.
func TestFormatNumber_FollowsTheReader(t *testing.T) {
	for name, tc := range map[string]struct {
		params   map[string]any
		in       any
		language string
		want     string
	}{
		"english plain":    {map[string]any{"op": "format"}, 1234567.891, "en", "1,234,567.89"},
		"swedish plain":    {map[string]any{"op": "format"}, 1234567.891, "sv", "1 234 567,89"},
		"english money":    {map[string]any{"op": "currency", "currency": "USD"}, 1234.5, "en", "$1,234.50"},
		"swedish money":    {map[string]any{"op": "currency", "currency": "SEK"}, 1234.5, "sv", "1 234,50 kr"},
		"english percent":  {map[string]any{"op": "percent", "decimals": 1}, 0.2537, "en", "25.4%"},
		"swedish percent":  {map[string]any{"op": "percent", "decimals": 1}, 0.2537, "sv", "25,4 %"},
		"unknown language": {map[string]any{"op": "format"}, 1234.5, "de", "1,234.50"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := numberText(t, tc.params, tc.in, tc.language); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The step's own Language wins over the flow's.
func TestFormatNumber_LocaleOverride(t *testing.T) {
	if got := numberText(t, map[string]any{"op": "format", "locale": "sv"}, 1234.5, "en"); got != "1 234,50" {
		t.Errorf("got %q, want the Swedish form", got)
	}
	if got := numberText(t, map[string]any{"op": "format", "locale": ""}, 1234.5, "sv"); got != "1 234,50" {
		t.Errorf("got %q — an empty override should fall back to the flow", got)
	}
}

// A separator you cannot see would survive an email and then break a
// spreadsheet lookup with nothing to show for it.
func TestFormatNumber_SeparatorsAreOrdinaryCharacters(t *testing.T) {
	got := numberText(t, map[string]any{"op": "format"}, 1234567.0, "sv")
	for _, r := range got {
		if r > 127 {
			t.Fatalf("%q contains a non-ASCII character (%U) — the grouping must stay typeable", got, r)
		}
	}
}

// Money that has no symbol of its own is written the way a bank writes it.
func TestFormatNumber_UnknownCurrencyUsesItsCode(t *testing.T) {
	if got := numberText(t, map[string]any{"op": "currency", "currency": "PLN"}, 1234.5, "en"); got != "PLN 1,234.50" {
		t.Errorf("got %q", got)
	}
	if got := numberText(t, map[string]any{"op": "currency", "currency": "pln"}, 1234.5, "sv"); got != "1 234,50 PLN" {
		t.Errorf("got %q — the code should be read whatever case it was typed in", got)
	}
}

func TestFormatNumber_NegativeMoneyKeepsItsSign(t *testing.T) {
	if got := numberText(t, map[string]any{"op": "currency", "currency": "USD"}, -42.0, "en"); got != "-$42.00" {
		t.Errorf("got %q", got)
	}
	if got := numberText(t, map[string]any{"op": "currency", "currency": "SEK"}, -42.0, "sv"); got != "-42,00 kr" {
		t.Errorf("got %q", got)
	}
}

// Rounding is the one that changes the number, so the number has to come out
// as a number — ready to multiply, not to be parsed back out of its own text.
func TestFormatNumber_RoundingEmitsANumber(t *testing.T) {
	for name, tc := range map[string]struct {
		mode     string
		decimals int
		in       float64
		want     float64
	}{
		"nearest up":     {"nearest", 0, 17.5, 18},
		"nearest down":   {"nearest", 0, 17.4, 17},
		"always up":      {"up", 0, 17.2, 18},
		"always down":    {"down", 0, 17.9, 17},
		"two decimals":   {"nearest", 2, 1.005999, 1.01},
		"up on negative": {"up", 0, -17.2, -17},
	} {
		t.Run(name, func(t *testing.T) {
			res := runNumber(t, map[string]any{"op": "round", "round_mode": tc.mode, "decimals": tc.decimals}, tc.in, "en")
			got, ok := res.Output["value"].Inline.(float64)
			if !ok {
				t.Fatalf("value is %T, want a number", res.Output["value"].Inline)
			}
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Every operation emits both, so a formatted price can still be compared.
func TestFormatNumber_BothPortsAlwaysFire(t *testing.T) {
	res := runNumber(t, map[string]any{"op": "currency", "currency": "SEK"}, 1234.5, "sv")
	if text, _ := res.Output["out"].Inline.(string); text == "" {
		t.Error("no text on 'Result'")
	}
	if n, _ := res.Output["value"].Inline.(float64); n != 1234.5 {
		t.Errorf("value = %v, want the number unchanged", res.Output["value"].Inline)
	}
}

func TestFormatNumber_Grouping(t *testing.T) {
	if got := numberText(t, map[string]any{"op": "format", "group": false}, 1234567.0, "en"); got != "1234567.00" {
		t.Errorf("got %q, want no grouping", got)
	}
	// Three digits or fewer have nothing to group.
	if got := numberText(t, map[string]any{"op": "format", "decimals": 0}, 999.0, "sv"); got != "999" {
		t.Errorf("got %q", got)
	}
}

// A number that arrived as text — from a form, a scraped page, a CSV — is
// still a number, and refusing it would send the author off to write a formula.
func TestFormatNumber_ReadsNumbersPeopleWrote(t *testing.T) {
	for in, want := range map[string]float64{
		"1234.5":        1234.5,
		"1 234,50":      1234.5,
		"1\u00a0234,50": 1234.5, // as copied out of a web page
		"1,234.50":      1234.5,
		"1.234,50":      1234.5,
		"1,23":          1.23,
		"1,234":         1234, // three digits after a lone comma reads as grouping
		"-42":           -42,
		"  7  ":         7,
	} {
		res := runNumber(t, map[string]any{"op": "format"}, in, "en")
		if res.Status != core.StatusOK {
			t.Errorf("%q: %+v", in, res.Error)
			continue
		}
		if got, _ := res.Output["value"].Inline.(float64); math.Abs(got-want) > 1e-9 {
			t.Errorf("%q read as %v, want %v", in, got, want)
		}
	}
}

func TestFormatNumber_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		params map[string]any
		in     any
		code   string
		says   string
	}{
		"no input":     {map[string]any{"op": "format"}, nil, "missing_input", ""},
		"not a number": {map[string]any{"op": "format"}, "twelve", "bad_input", "twelve"},
		"a list":       {map[string]any{"op": "format"}, []any{1, 2}, "bad_input", ""},
		"unknown op":   {map[string]any{"op": "square"}, 1.0, "bad_param", "square"},
		"not finite":   {map[string]any{"op": "format"}, math.Inf(1), "bad_input", "not a number"},
	} {
		t.Run(name, func(t *testing.T) {
			res := runNumber(t, tc.params, tc.in, "en")
			if res.Status != core.StatusError || res.Error.Code != tc.code {
				t.Fatalf("status=%q err=%+v, want %s", res.Status, res.Error, tc.code)
			}
			if tc.says != "" && !strings.Contains(res.Error.Message, tc.says) {
				t.Errorf("message = %q, want it to mention %q", res.Error.Message, tc.says)
			}
		})
	}
}
