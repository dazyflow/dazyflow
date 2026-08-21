// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package reltime parses the time values a person types into a step: an
// absolute timestamp, or a relative one like "tomorrow" or "+3d".
//
// Every scheduled flow that reads a time window wants the window to move with
// the schedule — "tomorrow's bookings", "the last 7 days of orders". A field
// that only accepts RFC3339 cannot express that at all, so the window either
// has to be left wide open and filtered afterwards, or re-typed by hand every
// day. This is the shared grammar those fields use instead.
//
//	now                    the moment the step runs
//	today / tomorrow /
//	yesterday              midnight at the start of that day, in the given zone
//	+3d / -2h30m / 1w      an offset from now
//	tomorrow+9h            an offset from a named day
//	2026-06-16T00:00:00Z   an absolute time (RFC3339, a plain date, or Unix seconds)
package reltime

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// inputLayouts are the absolute formats accepted, most specific first.
var inputLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// ParseOffset parses a signed duration that, on top of Go's h/m/s, also
// understands w (weeks) and d (days) — e.g. "3d", "-2h30m", "1w2d". An empty
// string is a zero offset.
func ParseOffset(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	sign := time.Duration(1)
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		sign = -1
		s = s[1:]
	}
	var total time.Duration
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return 0, fmt.Errorf("bad offset %q: expected a number before position %d", s, i)
		}
		n, err := strconv.Atoi(s[start:i])
		if err != nil {
			return 0, fmt.Errorf("bad offset %q: %v", s, err)
		}
		if i >= len(s) {
			return 0, fmt.Errorf("bad offset %q: number %d has no unit (use w, d, h, m, or s)", s, n)
		}
		unit := s[i]
		i++
		switch unit {
		case 'w':
			total += time.Duration(n) * 7 * 24 * time.Hour
		case 'd':
			total += time.Duration(n) * 24 * time.Hour
		case 'h':
			total += time.Duration(n) * time.Hour
		case 'm':
			total += time.Duration(n) * time.Minute
		case 's':
			total += time.Duration(n) * time.Second
		default:
			return 0, fmt.Errorf("bad offset %q: unknown unit %q (use w, d, h, m, or s)", s, string(unit))
		}
	}
	return sign * total, nil
}

// IsRelative reports whether s is written in the relative form — a leading
// sign ("+3d"), or one of the named days, with or without an offset. Callers
// that must preserve the exact shape of an absolute value (a plain date meaning
// an all-day calendar event, say) use this to resolve ONLY the relative ones.
func IsRelative(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		_, err := ParseOffset(s)
		return err == nil
	}
	base, _ := splitBase(s)
	switch strings.ToLower(strings.TrimSpace(base)) {
	case "now", "today", "tomorrow", "yesterday":
		return true
	}
	return false
}

// Resolve turns s into a concrete time. loc anchors the day boundaries of the
// named days (nil means UTC); now is the reference instant. An empty s returns
// the zero time and ok=false so callers can treat "unset" as "no bound".
func Resolve(s string, loc *time.Location, now time.Time) (t time.Time, ok bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	if loc == nil {
		loc = time.UTC
	}

	base, offsetPart := splitBase(s)
	var anchor time.Time
	switch strings.ToLower(base) {
	case "now":
		anchor = now
	case "today":
		anchor = startOfDay(now, loc, 0)
	case "tomorrow":
		anchor = startOfDay(now, loc, 1)
	case "yesterday":
		anchor = startOfDay(now, loc, -1)
	case "":
		// A bare offset ("+3d") is relative to now.
		anchor = now
	default:
		abs, aerr := parseAbsolute(base)
		if aerr != nil {
			return time.Time{}, false, aerr
		}
		anchor = abs
	}

	if offsetPart != "" {
		d, oerr := ParseOffset(offsetPart)
		if oerr != nil {
			return time.Time{}, false, oerr
		}
		anchor = anchor.Add(d)
	}
	return anchor.UTC(), true, nil
}

// ResolveRFC3339 is Resolve rendered for an API query parameter. Absolute
// values that already parse are re-rendered, so callers get one shape.
func ResolveRFC3339(s string, loc *time.Location, now time.Time) (string, error) {
	t, ok, err := Resolve(s, loc, now)
	if err != nil || !ok {
		return "", err
	}
	return t.Format(time.RFC3339), nil
}

// splitBase cuts s into its named/absolute base and a trailing signed offset.
// "tomorrow+9h" → ("tomorrow", "+9h"); "+3d" → ("", "+3d"); an absolute
// timestamp keeps its own sign-bearing zone suffix (e.g. "…+02:00") because
// the split only fires on a +/- that follows a letter or digit AND is not part
// of a time zone offset.
func splitBase(s string) (base, offset string) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != '+' && s[i] != '-' {
			continue
		}
		cand := s[i:]
		if _, err := ParseOffset(cand); err != nil {
			continue
		}
		// "2026-06-16" would otherwise split at its own dashes; an absolute
		// base only ever pairs with an offset when it is unambiguous, so
		// require the base to be a known day word.
		switch strings.ToLower(strings.TrimSpace(s[:i])) {
		case "", "now", "today", "tomorrow", "yesterday":
			return strings.TrimSpace(s[:i]), cand
		}
		return s, ""
	}
	return s, ""
}

// startOfDay is midnight in loc, dayOffset days from now.
func startOfDay(now time.Time, loc *time.Location, dayOffset int) time.Time {
	local := now.In(loc).AddDate(0, 0, dayOffset)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

func parseAbsolute(s string) (time.Time, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range inputLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("couldn't read %q as a time — use a date (2026-06-16), a full timestamp (2026-06-16T09:00:00Z), or a relative value like \"tomorrow\", \"now+2h\" or \"-7d\"", s)
}
