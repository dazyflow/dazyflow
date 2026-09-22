// SPDX-FileCopyrightText: 2026 Angels' Ware
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

var inputLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

func ParseOffset(s string) (time.Duration, error) {
	days, sub, err := parseOffsetParts(s)
	return time.Duration(days)*24*time.Hour + sub, err
}

// parseOffsetParts splits an offset into whole days (weeks count as 7) and the
// sub-day rest, both carrying the sign, so a caller anchored to a calendar day
// can apply them as wall-clock changes rather than fixed 24-hour blocks.
func parseOffsetParts(s string) (days int, sub time.Duration, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	sign := 1
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		sign = -1
		s = s[1:]
	}
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return 0, 0, fmt.Errorf("bad offset %q: expected a number before position %d", s, i)
		}
		n, err := strconv.Atoi(s[start:i])
		if err != nil {
			return 0, 0, fmt.Errorf("bad offset %q: %v", s, err)
		}
		if i >= len(s) {
			return 0, 0, fmt.Errorf("bad offset %q: number %d has no unit (use w, d, h, m, or s)", s, n)
		}
		unit := s[i]
		i++
		switch unit {
		case 'w':
			days += n * 7
		case 'd':
			days += n
		case 'h':
			sub += time.Duration(n) * time.Hour
		case 'm':
			sub += time.Duration(n) * time.Minute
		case 's':
			sub += time.Duration(n) * time.Second
		default:
			return 0, 0, fmt.Errorf("bad offset %q: unknown unit %q (use w, d, h, m, or s)", s, string(unit))
		}
	}
	return sign * days, time.Duration(sign) * sub, nil
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
	dayAnchored := false
	switch strings.ToLower(base) {
	case "now":
		anchor = now
	case "today":
		anchor, dayAnchored = startOfDay(now, loc, 0), true
	case "tomorrow":
		anchor, dayAnchored = startOfDay(now, loc, 1), true
	case "yesterday":
		anchor, dayAnchored = startOfDay(now, loc, -1), true
	case "":
		anchor = now
	default:
		abs, aerr := parseAbsolute(base, loc)
		if aerr != nil {
			return time.Time{}, false, aerr
		}
		anchor = abs
	}

	if offsetPart != "" {
		days, sub, oerr := parseOffsetParts(offsetPart)
		if oerr != nil {
			return time.Time{}, false, oerr
		}
		if dayAnchored {
			// Wall clock from the day's midnight: "tomorrow+9h" is nine o'clock
			// tomorrow even across a DST change, where adding 9 hours of
			// elapsed time would land at 08:00 or 10:00.
			anchor = time.Date(anchor.Year(), anchor.Month(), anchor.Day()+days,
				0, 0, 0, int(sub), loc)
		} else {
			anchor = anchor.Add(time.Duration(days)*24*time.Hour + sub)
		}
	}
	return anchor.UTC(), true, nil
}

func ResolveRFC3339(s string, loc *time.Location, now time.Time) (string, error) {
	t, ok, err := Resolve(s, loc, now)
	if err != nil || !ok {
		return "", err
	}
	return t.Format(time.RFC3339), nil
}

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

func startOfDay(now time.Time, loc *time.Location, dayOffset int) time.Time {
	local := now.In(loc).AddDate(0, 0, dayOffset)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

// parseAbsolute reads a zoneless timestamp or plain date in loc: "2026-06-16"
// for a step set to Europe/Stockholm is midnight there, not midnight UTC.
func parseAbsolute(s string, loc *time.Location) (time.Time, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range inputLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("couldn't read %q as a time — use a date (2026-06-16), a full timestamp (2026-06-16T09:00:00Z), or a relative value like \"tomorrow\", \"now+2h\" or \"-7d\"", s)
}
