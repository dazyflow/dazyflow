// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package reltime

import (
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	sthlm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	// A Thursday afternoon in Stockholm (UTC+2 in June).
	now := time.Date(2026, 6, 18, 14, 30, 0, 0, sthlm)

	cases := []struct {
		in   string
		want string // RFC3339 in UTC
	}{
		{"now", "2026-06-18T12:30:00Z"},
		{"today", "2026-06-17T22:00:00Z"},    // midnight the 18th, Stockholm
		{"tomorrow", "2026-06-18T22:00:00Z"}, // midnight the 19th, Stockholm
		{"yesterday", "2026-06-16T22:00:00Z"},
		{"tomorrow+1d", "2026-06-19T22:00:00Z"},
		{"tomorrow+9h", "2026-06-19T07:00:00Z"},
		{"+3d", "2026-06-21T12:30:00Z"},
		{"-7d", "2026-06-11T12:30:00Z"},
		{"now-2h30m", "2026-06-18T10:00:00Z"},
		{"TOMORROW", "2026-06-18T22:00:00Z"},
		// Absolute values pass through untouched, zone offset and all.
		{"2026-06-16T00:00:00Z", "2026-06-16T00:00:00Z"},
		{"2026-06-16T09:00:00+02:00", "2026-06-16T07:00:00Z"},
		{"2026-06-16", "2026-06-16T00:00:00Z"},
	}
	for _, c := range cases {
		got, err := ResolveRFC3339(c.in, sthlm, now)
		if err != nil {
			t.Errorf("Resolve(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Resolve(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestResolve_EmptyIsUnset(t *testing.T) {
	if _, ok, err := Resolve("   ", nil, time.Now()); ok || err != nil {
		t.Errorf("empty = (ok %v, err %v), want (false, nil)", ok, err)
	}
}

func TestResolve_BadValue(t *testing.T) {
	if _, _, err := Resolve("next thursday", nil, time.Now()); err == nil {
		t.Error("expected an error naming the accepted forms")
	}
	if _, _, err := Resolve("tomorrow+3q", nil, time.Now()); err == nil {
		t.Error("expected an error on the unknown unit")
	}
}

func TestParseOffset(t *testing.T) {
	cases := map[string]time.Duration{
		"":       0,
		"3d":     72 * time.Hour,
		"-2h30m": -(2*time.Hour + 30*time.Minute),
		"1w2d":   9 * 24 * time.Hour,
		"+45s":   45 * time.Second,
	}
	for in, want := range cases {
		got, err := ParseOffset(in)
		if err != nil {
			t.Errorf("ParseOffset(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseOffset(%q) = %v, want %v", in, got, want)
		}
	}
}
