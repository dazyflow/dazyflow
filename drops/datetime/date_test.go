// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package datetime

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
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
	res := runDate(t, "2026-01-15T12:00:00Z", map[string]any{"tz": "America/New_York", "format": "time24"})
	if got := outOf(t, res); got != "07:00:00" {
		t.Errorf("ny time = %q, want 07:00:00", got)
	}
}

func TestDate_CustomLayout(t *testing.T) {
	res := runDate(t, "2026-07-02T00:00:00Z", map[string]any{
		"format": "custom", "custom_format": "ddd D MMM YYYY",
	})
	if got := outOf(t, res); got != "Thu 2 Jul 2026" {
		t.Errorf("custom = %q, want Thu 2 Jul 2026", got)
	}
}

func TestDate_NoInputUsesNow(t *testing.T) {
	before := time.Now().UTC().Add(-2 * time.Second)
	res := runDate(t, nil, map[string]any{"format": "unix"})
	got := outOf(t, res)
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

// "Tomorrow" is the case this step gets reached for most, and the one the
// offset alone gets subtly wrong: "1d" lands at whatever time the flow ran, so
// a deadline written from it drifts by however late in the day it fired. 'at'
// pins the clock, in the OUTPUT timezone.
func TestDate_TomorrowAtNineLocal(t *testing.T) {
	res := runDate(t, "2026-08-27T23:40:00Z", map[string]any{
		"add":           "1d",
		"at":            "09:00",
		"tz":            "Europe/Stockholm",
		"format":        "custom",
		"custom_format": "DD/MM/YYYY HH:mm",
	})
	if got := outOf(t, res); got != "29/08/2026 09:00" {
		t.Errorf("got %q, want 29/08/2026 09:00", got)
	}
}

func TestDate_AtSetsTheClockInTheOutputZone(t *testing.T) {
	res := runDate(t, "2026-08-27T23:40:00Z", map[string]any{
		"at": "09:00", "tz": "Europe/Stockholm", "format": "iso",
	})
	if got := outOf(t, res); got != "2026-08-28T09:00:00+02:00" {
		t.Errorf("got %q, want 2026-08-28T09:00:00+02:00", got)
	}
}

func TestDate_AtStartOfDay(t *testing.T) {
	res := runDate(t, "2026-08-27T13:45:07Z", map[string]any{"at": "00:00", "format": "datetime"})
	if got := outOf(t, res); got != "2026-08-27 00:00:00" {
		t.Errorf("got %q, want 2026-08-27 00:00:00", got)
	}
}

func TestDate_AtRejectsNonsense(t *testing.T) {
	res := runDate(t, "2026-08-27T13:45:07Z", map[string]any{"at": "25:00"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want error/bad_param", res)
	}
}

func TestDate_CustomFormat(t *testing.T) {
	res := runDate(t, "2026-08-27T13:45:07Z", map[string]any{
		"format": "custom", "custom_format": "ddd D MMM YYYY",
	})
	if got := outOf(t, res); got != "Thu 27 Aug 2026" {
		t.Errorf("got %q, want Thu 27 Aug 2026", got)
	}
}

func TestDate_CustomFormatRejectsUnknownToken(t *testing.T) {
	res := runDate(t, "2026-08-27T13:45:07Z", map[string]any{
		"format": "custom", "custom_format": "YYYY-MM-DD at HH:mm",
	})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want error/bad_param (\"at\" is not a token — it needs brackets)", res)
	}
}

// Half-configured rather than wrong: Custom picked, nothing written. Falling
// back to ISO would be a silent guess at what the flow author meant.
func TestDate_CustomWithNoFormatIsAnError(t *testing.T) {
	res := runDate(t, "2026-08-27T13:45:07Z", map[string]any{"format": "custom"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want error/bad_param", res)
	}
}

func TestDate_OffEnumFormatIsAnError(t *testing.T) {
	for _, f := range []string{"Mon 2 Jan 2006", "DD/MM/YYYY", "2006-01-02", "sometime soon", "kitchen"} {
		res := runDate(t, "2026-08-27T13:45:07Z", map[string]any{"format": f})
		if res.Status != core.StatusError || res.Error.Code != "bad_param" {
			t.Errorf("format %q: res = %+v, want error/bad_param", f, res)
		}
	}
}

func TestDate_TimeFormats(t *testing.T) {
	for _, c := range []struct{ format, want string }{
		{"time24", "14:05:09"},
		{"time12", "2:05:09 PM"},
	} {
		res := runDate(t, "2026-08-27T14:05:09Z", map[string]any{"format": c.format})
		if got := outOf(t, res); got != c.want {
			t.Errorf("format %q = %q, want %q", c.format, got, c.want)
		}
	}
}

func TestDate_TimezoneFromDropdown(t *testing.T) {
	res := runDate(t, "2026-08-27T14:05:09Z", map[string]any{
		"tz": "Asia/Tokyo", "format": "datetime",
	})
	if got := outOf(t, res); got != "2026-08-27 23:05:09" {
		t.Errorf("got %q, want 2026-08-27 23:05:09", got)
	}
}

// The picker offers every IANA zone the browser knows, and the step accepts
// any of them — including the far ones no curated list would have carried.
func TestDate_TimezoneAnyIANAZone(t *testing.T) {
	res := runDate(t, "2026-08-27T14:05:09Z", map[string]any{
		"tz": "Pacific/Auckland", "format": "datetime",
	})
	if got := outOf(t, res); got != "2026-08-28 02:05:09" {
		t.Errorf("got %q, want 2026-08-28 02:05:09", got)
	}
}

func TestDate_TimezoneRejectsNonsense(t *testing.T) {
	res := runDate(t, "2026-08-27T14:05:09Z", map[string]any{"tz": "Mars/Olympus"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want error/bad_param", res)
	}
}

func TestDate_LegacyArbitraryTimezone(t *testing.T) {
	res := runDate(t, "2026-08-27T14:05:09Z", map[string]any{
		"tz": "Africa/Nairobi", "format": "datetime",
	})
	if got := outOf(t, res); got != "2026-08-27 17:05:09" {
		t.Errorf("got %q, want 2026-08-27 17:05:09", got)
	}
}

func TestDate_WeekdayFormat(t *testing.T) {
	for _, c := range []struct{ format, want string }{
		{"weekday", "Thursday"},
		{"weekday_short", "Thu"},
	} {
		res := runDate(t, "2026-08-27T14:05:09Z", map[string]any{"format": c.format})
		if got := outOf(t, res); got != c.want {
			t.Errorf("format %q = %q, want %q", c.format, got, c.want)
		}
	}
}

func TestDate_WeekdayFormatWithOffset(t *testing.T) {
	res := runDate(t, "2026-08-31T09:00:00Z", map[string]any{ // a Monday
		"add": "1d", "format": "weekday",
	})
	if got := outOf(t, res); got != "Tuesday" {
		t.Errorf("got %q, want Tuesday", got)
	}
}

func TestDate_WeekdayFormatUsesTheOutputTimezone(t *testing.T) {
	res := runDate(t, "2026-08-31T23:00:00Z", map[string]any{ // Mon 23:00Z
		"tz": "Australia/Sydney", "format": "weekday",
	})
	if got := outOf(t, res); got != "Tuesday" {
		t.Errorf("got %q, want Tuesday", got)
	}
}
