// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func runLookup(t *testing.T, p map[string]any, in any) core.Result {
	t.Helper()
	job := core.Job{ID: "t", Params: p}
	if in != nil {
		job.Input = map[string]core.Ref{"in": {Inline: in}}
	}
	res, err := executeLookup(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("executeLookup: %v", err)
	}
	return res
}

var countries = map[string]any{"SE": "Sweden", "NO": "Norway"}

func TestLookup_MapsAValue(t *testing.T) {
	res := runLookup(t, map[string]any{"table": countries}, "SE")
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "Sweden" {
		t.Errorf("out = %v, want Sweden", got)
	}
	if res.Output["out"].MIME != "text/plain" {
		t.Errorf("MIME = %q, want text/plain", res.Output["out"].MIME)
	}
	if _, fired := res.Output["unmatched"]; fired {
		t.Error("'No match' fired on a hit")
	}
}

// Capitalisation is ignored by default, which is what makes a table written by
// hand match values arriving from an API.
func TestLookup_IgnoresCapitalisationUnlessTold(t *testing.T) {
	if got := runLookup(t, map[string]any{"table": countries}, "se").Output["out"].Inline; got != "Sweden" {
		t.Errorf("out = %v, want Sweden", got)
	}
	res := runLookup(t, map[string]any{"table": countries, "case_sensitive": true}, "se")
	if _, fired := res.Output["unmatched"]; !fired {
		t.Errorf("with capitalisation on, \"se\" should not find \"SE\": %v", res.Output)
	}
}

// Exactly one of the two outputs fires — never both, never neither.
func TestLookup_UnmatchedGoesOneWayOrTheOther(t *testing.T) {
	withFallback := runLookup(t, map[string]any{"table": countries, "fallback": "Unknown"}, "DK")
	if got := withFallback.Output["out"].Inline; got != "Unknown" {
		t.Errorf("out = %v, want the fallback", got)
	}
	if _, fired := withFallback.Output["unmatched"]; fired {
		t.Error("'No match' fired even though a fallback was set")
	}

	without := runLookup(t, map[string]any{"table": countries}, "DK")
	if _, fired := without.Output["out"]; fired {
		t.Error("'Result' fired with no match and no fallback")
	}
	if got := without.Output["unmatched"].Inline; got != "DK" {
		t.Errorf("unmatched = %v, want the value that came in", got)
	}
}

// A number wired in from an earlier step still finds the row someone typed.
func TestLookup_MatchesANumberAgainstATypedTable(t *testing.T) {
	table := map[string]any{"200": "ok", "404": "missing"}
	if got := runLookup(t, map[string]any{"table": table}, float64(404)).Output["out"].Inline; got != "missing" {
		t.Errorf("out = %v, want missing", got)
	}
}

func TestLookup_RejectsWhatCannotWork(t *testing.T) {
	for name, tc := range map[string]struct {
		params map[string]any
		in     any
		code   string
	}{
		"no table":    {map[string]any{}, "SE", "bad_param"},
		"empty table": {map[string]any{"table": map[string]any{}}, "SE", "bad_param"},
		"not a table": {map[string]any{"table": "SE=Sweden"}, "SE", "bad_param"},
		"no input":    {map[string]any{"table": countries}, nil, "missing_input"},
	} {
		t.Run(name, func(t *testing.T) {
			res := runLookup(t, tc.params, tc.in)
			if res.Status != core.StatusError || res.Error.Code != tc.code {
				t.Fatalf("status=%q err=%+v, want %s", res.Status, res.Error, tc.code)
			}
		})
	}
}
