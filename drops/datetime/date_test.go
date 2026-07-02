// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package datetime

import (
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runDate(t *testing.T, in any, params map[string]any) core.Result {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	job := core.Job{ID: "test", Params: params}
	if in != nil {
		job.Input = map[string]core.Ref{"in": {Inline: in}}
	}
	res, err := executeDate(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("executeDate returned error: %v", err)
	}
	return res
}

func outOf(t *testing.T, res core.Result) string {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	s, ok := res.Output["out"].Inline.(string)
	if !ok {
		t.Fatalf("out is %T, want string", res.Output["out"].Inline)
	}
	return s
}

func TestDate_ParseISOAndReformat(t *testing.T) {
	res := runDate(t, "2026-07-02T13:45:07Z", map[string]any{"format": "date"})
	if got := outOf(t, res); got != "2026-07-02" {
		t.Errorf("date = %q, want 2026-07-02", got)
	}
}

func TestDate_UnixInput(t *testing.T) {
	// 1_700_000_000 = 2023-11-14T22:13:20Z
	res := runDate(t, float64(1_700_000_000), map[string]any{"format": "datetime"})
	if got := outOf(t, res); got != "2023-11-14 22:13:20" {
		t.Errorf("datetime = %q, want 2023-11-14 22:13:20", got)
	}
}

func TestDate_UnixStringInput(t *testing.T) {
	res := runDate(t, "1700000000", map[string]any{"format": "unix"})
	if got := outOf(t, res); got != "1700000000" {
		t.Errorf("unix = %q, want 1700000000", got)
	}
}

func TestDate_OffsetDaysAndHours(t *testing.T) {
	res := runDate(t, "2026-07-02T00:00:00Z", map[string]any{"add": "3d12h", "format": "datetime"})
	if got := outOf(t, res); got != "2026-07-05 12:00:00" {
		t.Errorf("shifted = %q, want 2026-07-05 12:00:00", got)
	}
}

func TestDate_NegativeOffset(t *testing.T) {
	res := runDate(t, "2026-07-02T00:00:00Z", map[string]any{"add": "-1w", "format": "date"})
	if got := outOf(t, res); got != "2026-06-25" {
		t.Errorf("shifted = %q, want 2026-06-25", got)
	}
}

func TestDate_Timezone(t *testing.T) {
	// 2026-01-15T12:00:00Z in New York (EST, UTC-5) is 07:00.
	res := runDate(t, "2026-01-15T12:00:00Z", map[string]any{"tz": "America/New_York", "format": "time"})
	if got := outOf(t, res); got != "07:00:00" {
		t.Errorf("ny time = %q, want 07:00:00", got)
	}
}

func TestDate_CustomLayout(t *testing.T) {
	res := runDate(t, "2026-07-02T00:00:00Z", map[string]any{"format": "Mon 2 Jan 2006"})
	if got := outOf(t, res); got != "Thu 2 Jul 2026" {
		t.Errorf("custom = %q, want Thu 2 Jul 2026", got)
	}
}

func TestDate_NoInputUsesNow(t *testing.T) {
	before := time.Now().UTC().Add(-2 * time.Second)
	res := runDate(t, nil, map[string]any{"format": "unix"})
	got := outOf(t, res)
	// Just assert it's a plausible current unix timestamp (>= a moment ago).
	if len(got) < 10 {
		t.Fatalf("unix now = %q looks wrong", got)
	}
	res2 := runDate(t, nil, map[string]any{"format": "iso"})
	parsed, err := time.Parse(time.RFC3339, outOf(t, res2))
	if err != nil {
		t.Fatalf("iso now didn't parse: %v", err)
	}
	if parsed.Before(before) {
		t.Errorf("now (%v) is before test start (%v)", parsed, before)
	}
}

func TestDate_PartsOutput(t *testing.T) {
	res := runDate(t, "2026-07-02T13:45:07Z", nil)
	parts, ok := res.Output["value"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want map", res.Output["value"].Inline)
	}
	if parts["year"] != 2026 || parts["month"] != 7 || parts["day"] != 2 {
		t.Errorf("parts date = %v-%v-%v, want 2026-7-2", parts["year"], parts["month"], parts["day"])
	}
	if parts["weekday"] != "Thursday" {
		t.Errorf("weekday = %v, want Thursday", parts["weekday"])
	}
}

func TestDate_BadInput(t *testing.T) {
	res := runDate(t, "not a date", nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}

func TestDate_BadOffset(t *testing.T) {
	res := runDate(t, "2026-07-02T00:00:00Z", map[string]any{"add": "3x"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status/code = %v/%v, want error/bad_param", res.Status, res.Error)
	}
}

func TestDate_BadTimezone(t *testing.T) {
	res := runDate(t, "2026-07-02T00:00:00Z", map[string]any{"tz": "Mars/Olympus"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status/code = %v/%v, want error/bad_param", res.Status, res.Error)
	}
}

func TestParseOffset(t *testing.T) {
	cases := map[string]time.Duration{
		"":       0,
		"24h":    24 * time.Hour,
		"3d":     3 * 24 * time.Hour,
		"1w":     7 * 24 * time.Hour,
		"-2h30m": -(2*time.Hour + 30*time.Minute),
		"1w2d3h": 7*24*time.Hour + 2*24*time.Hour + 3*time.Hour,
		"+15m":   15 * time.Minute,
	}
	for in, want := range cases {
		got, err := parseOffset(in)
		if err != nil {
			t.Errorf("parseOffset(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseOffset(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"3x", "d", "12", "1.5h", "abc"} {
		if _, err := parseOffset(bad); err == nil {
			t.Errorf("parseOffset(%q) should have errored", bad)
		}
	}
}
