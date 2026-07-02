// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package datetime hosts date/time drops — pure, no-auth nodes for the
// everyday time work a flow needs: read "now", parse a timestamp that
// arrived as text, shift it by an offset, convert it to a timezone, and
// render it in a chosen format. They speak the same text/rows contract as
// the transform family, so a formatted date drops straight into an email
// body, a filename, a Sheets cell, or a comparison.
package datetime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "date",
			Version:     "1.0",
			Label:       "Date & time",
			Subtitle:    "Format or shift a date",
			Color:       "#888",
			Icon:        "calendar-clock",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"date", "time", "datetime", "now", "timestamp", "format", "timezone"},
			Description: "Work with a date/time: read the current time, parse a timestamp that came in as text, shift it by an offset, convert it to a timezone, and render it in the format you want. Wire a value into 'in' (an ISO-8601 string, a Unix timestamp, or common date text) or leave it unwired to use the current time. 'add' shifts by an offset like \"3d\", \"-2h30m\", or \"1w\"; 'tz' names an IANA timezone (e.g. \"Europe/Stockholm\") for the output; 'format' picks a preset (iso, date, time, datetime, unix, rfc1123, kitchen) or a custom Go layout. Emits the formatted string on 'out' and the broken-out parts (year, month, weekday, …) on 'value'.",
			Summary:     "Read/parse a date, shift and re-timezone it, and render it in a chosen format.",
			Examples: []core.ParamsExample{
				{
					Title:  "Today's date as YYYY-MM-DD",
					Params: json.RawMessage(`{"format":"date"}`),
					Notes:  "No input wired → uses the current time. Great for datestamped filenames or subjects.",
				},
				{
					Title:  "Deadline 3 days from now, Stockholm time",
					Params: json.RawMessage(`{"add":"3d","tz":"Europe/Stockholm","format":"datetime"}`),
				},
				{
					Title:  "Reformat an incoming timestamp",
					Params: json.RawMessage(`{"format":"rfc1123"}`),
					Notes:  "Wire an ISO-8601 string or Unix seconds into 'in'; it's parsed then re-rendered.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "Date", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Formatted", MIME: []string{"text/plain"}},
				{Port: "value", Label: "Parts", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"add":    {"type":"string","title":"Offset","description":"Shift the time by an offset, e.g. \"3d\", \"-2h30m\", \"1w\". Units: w (weeks), d (days), h, m, s. Empty = no shift."},
					"tz":     {"type":"string","title":"Timezone","description":"IANA timezone for the output, e.g. \"Europe/Stockholm\", \"America/New_York\". Empty or \"UTC\" = UTC."},
					"format": {"type":"string","title":"Format","default":"iso","description":"Output format: a preset (iso, date, time, datetime, unix, unixms, rfc1123, kitchen) or a custom Go reference layout like \"Mon 2 Jan 2006\"."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeDate,
	})
}

// executeDate resolves the base time (the 'in' input, or now when unwired),
// applies the timezone and offset, then renders the result per 'format'.
func executeDate(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// Base time: parse the 'in' input if present and non-empty, else now.
	base := time.Now().UTC()
	if ref, ok := job.Input["in"]; ok && ref.Inline != nil {
		if !isEmptyInput(ref.Inline) {
			t, err := parseTime(ref.Inline)
			if err != nil {
				return params.Err(job, "bad_input", err.Error()), nil
			}
			base = t
		}
	}

	// Offset.
	if add := strings.TrimSpace(params.StringDefault(job.Params, "add", "")); add != "" {
		d, err := parseOffset(add)
		if err != nil {
			return params.Err(job, "bad_param", err.Error()), nil
		}
		base = base.Add(d)
	}

	// Timezone for the output rendering.
	loc, err := loadLocation(params.StringDefault(job.Params, "tz", ""))
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	t := base.In(loc)

	// Render.
	format := strings.TrimSpace(params.StringDefault(job.Params, "format", "iso"))
	out := renderTime(t, format)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out":   {MIME: "text/plain", Inline: out},
			"value": {MIME: "application/json", Inline: timeParts(t)},
		},
	}, nil
}

// isEmptyInput reports whether the wired value carries no usable time — an
// empty/whitespace string. A non-string (number, object) is never "empty".
func isEmptyInput(inline any) bool {
	s, ok := inline.(string)
	return ok && strings.TrimSpace(s) == ""
}

// parseTime turns an inline value into a time. Numbers (and numeric strings)
// are read as Unix seconds; strings are tried against a set of common
// layouts, RFC3339 first.
func parseTime(inline any) (time.Time, error) {
	switch v := inline.(type) {
	case float64:
		return time.Unix(int64(v), 0).UTC(), nil
	case int:
		return time.Unix(int64(v), 0).UTC(), nil
	case int64:
		return time.Unix(v, 0).UTC(), nil
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return time.Unix(n, 0).UTC(), nil
		}
	case string:
		return parseTimeString(v)
	}
	return time.Time{}, fmt.Errorf("can't read a date from %T", inline)
}

// inputLayouts are tried in order when parsing a date string.
var inputLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02",
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822,
	"02 Jan 2006",
	time.Kitchen,
}

func parseTimeString(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty date string")
	}
	// A bare integer string is Unix seconds.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range inputLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("couldn't parse %q as a date — expected ISO-8601 (2006-01-02T15:04:05Z), a plain date, or Unix seconds", s)
}

// parseOffset parses a signed duration that, on top of Go's h/m/s, also
// understands w (weeks) and d (days) — e.g. "3d", "-2h30m", "1w2d". An
// empty string is a zero offset.
func parseOffset(s string) (time.Duration, error) {
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
		// Read the number.
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

// loadLocation resolves a timezone name. Empty and "UTC" are UTC; "Local" is
// the host's zone; anything else goes through the IANA database.
func loadLocation(tz string) (*time.Location, error) {
	switch strings.TrimSpace(tz) {
	case "", "UTC":
		return time.UTC, nil
	case "Local":
		return time.Local, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q — use an IANA name like \"Europe/Stockholm\"", tz)
	}
	return loc, nil
}

// renderTime formats t per a preset name or, failing that, a custom Go
// reference layout passed through verbatim.
func renderTime(t time.Time, format string) string {
	switch strings.ToLower(format) {
	case "", "iso", "rfc3339":
		return t.Format(time.RFC3339)
	case "date":
		return t.Format("2006-01-02")
	case "time":
		return t.Format("15:04:05")
	case "datetime":
		return t.Format("2006-01-02 15:04:05")
	case "unix":
		return strconv.FormatInt(t.Unix(), 10)
	case "unixms":
		return strconv.FormatInt(t.UnixMilli(), 10)
	case "rfc1123":
		return t.Format(time.RFC1123)
	case "kitchen":
		return t.Format(time.Kitchen)
	default:
		// A custom Go reference layout, e.g. "Mon 2 Jan 2006".
		return t.Format(format)
	}
}

// timeParts breaks a time into the fields a downstream drop is likely to
// branch or compute on, plus the canonical iso/unix renderings.
func timeParts(t time.Time) map[string]any {
	return map[string]any{
		"iso":     t.Format(time.RFC3339),
		"unix":    t.Unix(),
		"year":    t.Year(),
		"month":   int(t.Month()),
		"day":     t.Day(),
		"hour":    t.Hour(),
		"minute":  t.Minute(),
		"second":  t.Second(),
		"weekday": t.Weekday().String(),
		"tz":      t.Location().String(),
	}
}
