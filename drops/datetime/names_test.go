// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package datetime

import (
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/datenames"
)

func TestRenderCustom_Localized(t *testing.T) {
	sv := datenames.For("sv")
	cases := []struct{ format, want string }{
		{"dddd", "torsdag"},
		{"ddd", "tors"},
		{"D MMMM YYYY", "27 augusti 2026"},
		{"ddd D MMM", "tors 27 aug"},
		// Numbers and separators are the same in every language.
		{"YYYY-MM-DD HH:mm", "2026-08-27 14:05"},
		// A bracketed literal is the author's own words — never translated.
		{"[vecka] D MMM", "vecka 27 aug"},
	}
	for _, c := range cases {
		got, err := renderCustom(ref, c.format, sv)
		if err != nil {
			t.Fatalf("renderCustom(%q): %v", c.format, err)
		}
		if got != c.want {
			t.Errorf("renderCustom(%q) = %q, want %q", c.format, got, c.want)
		}
	}
}

func TestDate_LocaleParam(t *testing.T) {
	res := runDate(t, "2026-08-27T14:05:09Z", map[string]any{
		"locale": "sv", "format": "weekday",
	})
	if got := outOf(t, res); got != "torsdag" {
		t.Errorf("weekday = %q, want torsdag", got)
	}
	res = runDate(t, "2026-08-27T14:05:09Z", map[string]any{
		"locale": "sv", "format": "custom", "custom_format": "dddd D MMMM",
	})
	if got := outOf(t, res); got != "torsdag 27 augusti" {
		t.Errorf("custom = %q, want torsdag 27 augusti", got)
	}
}

// The machine formats are wire formats read by machines, not prose: RFC 1123
// MANDATES English abbreviations, so a Swedish Date: header would be malformed
// rather than translated.
func TestDate_MachineFormatsStayEnglish(t *testing.T) {
	for _, format := range []string{"iso", "unix", "unixms", "rfc1123"} {
		en := outOf(t, runDate(t, "2026-08-27T14:05:09Z", map[string]any{"format": format}))
		sv := outOf(t, runDate(t, "2026-08-27T14:05:09Z", map[string]any{"format": format, "locale": "sv"}))
		if en != sv {
			t.Errorf("format %q changed with the locale: %q vs %q", format, en, sv)
		}
	}
	// And rfc1123 really does carry the English day name.
	out := outOf(t, runDate(t, "2026-08-27T14:05:09Z", map[string]any{"format": "rfc1123", "locale": "sv"}))
	if !strings.HasPrefix(out, "Thu,") {
		t.Errorf("rfc1123 = %q, want it to start with Thu,", out)
	}
}

// The flow's language is the default, so a Swedish flow writes Swedish dates
// without setting the field on every date step.
func TestDate_FollowsTheFlowLanguage(t *testing.T) {
	job := core.Job{
		ID:       "j",
		Language: "sv",
		Params:   map[string]any{"format": "weekday"},
		Input:    map[string]core.Ref{"in": {Inline: "2026-08-27T14:05:09Z"}},
	}
	res, err := executeDate(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := outOf(t, res); got != "torsdag" {
		t.Errorf("got %q, want torsdag (the flow's language)", got)
	}
}

// And a step that names a language wins over the flow's, for the one message
// that has to differ.
func TestDate_StepLocaleOverridesTheFlow(t *testing.T) {
	job := core.Job{
		ID:       "j",
		Language: "sv",
		Params:   map[string]any{"format": "weekday", "locale": "en"},
		Input:    map[string]core.Ref{"in": {Inline: "2026-08-27T14:05:09Z"}},
	}
	res, err := executeDate(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := outOf(t, res); got != "Thursday" {
		t.Errorf("got %q, want Thursday (the step's own choice)", got)
	}
}

// A Go reference layout saved in an older flow asked for English by
// construction ("Mon 2 Jan 2006" names English months). Localizing it would
// silently change what those flows already send.
func TestDate_LegacyGoLayoutStaysEnglish(t *testing.T) {
	res := runDate(t, "2026-08-27T14:05:09Z", map[string]any{
		"format": "Mon 2 Jan 2006", "locale": "sv",
	})
	if got := outOf(t, res); got != "Thu 27 Aug 2026" {
		t.Errorf("got %q, want Thu 27 Aug 2026", got)
	}
}
