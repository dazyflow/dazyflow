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
	"git.sr.ht/~klahr/dazyflow/drops/internal/reltime"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/internal/datenames"
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
			Description: "Work with a date/time: read the current time, parse a timestamp that came in as text, shift it by an offset, pin it to a time of day, convert it to a timezone, and render it in the format you want. Connect a value into 'in' (an ISO-8601 string, a Unix timestamp, or common date text) or leave it unwired to use the current time. The steps apply in that order, which is what lets them combine: 'add' shifts by \"3d\", \"-2h30m\" or \"1w\" — so \"1d\" is tomorrow — and 'at' then sets the clock (\"09:00\"), which is what turns \"24 hours from now\" into \"tomorrow morning\". 'tz' picks the timezone the output is written in — search any IANA zone — and it decides which day and which hour, not just how the offset is labelled. 'locale' writes day and month names in a language — English or Swedish, following the flow's own language unless this step overrides it (the ISO/Unix/email formats stay English, being machine formats). 'format' picks a named format — date, date and time, a 12- or 24-hour clock, the weekday's name (\"Thursday\" / \"torsdag\"), Unix, email/HTTP — or Custom for one you write from YYYY MM DD HH mm ss tokens (\"DD/MM/YYYY\", \"ddd D MMM\"), with literal words in brackets (\"[week of] D MMM\"). Emits the formatted string on 'out' and the broken-out parts (year, month, weekday, …) on 'value'; drop it into any text with ${upstream.<step>.out}.",
			Summary:     "Read/parse a date, shift and re-timezone it, and render it in a chosen format.",
			Examples: []core.ParamsExample{
				{
					Title:  "Today's date as YYYY-MM-DD",
					Params: json.RawMessage(`{"format":"date"}`),
					Notes:  "No input connected → uses the current time. Great for datestamped filenames or subjects.",
				},
				{
					Title:  "Deadline 3 days from now, Stockholm time",
					Params: json.RawMessage(`{"add":"3d","tz":"Europe/Stockholm","format":"datetime"}`),
				},
				{
					Title:  "Tomorrow, written the way a person writes it",
					Params: json.RawMessage(`{"add":"1d","format":"custom","custom_format":"ddd D MMM YYYY"}`),
					Notes:  "Renders e.g. \"Fri 28 Aug 2026\". Reference it from any text field with ${upstream.<this step's id>.out}.",
				},
				{
					Title:  "Tomorrow at nine, local time",
					Params: json.RawMessage(`{"add":"1d","at":"09:00","tz":"Europe/Stockholm","format":"custom","custom_format":"DD/MM/YYYY HH:mm"}`),
					Notes:  "The offset moves the day; 'at' sets the clock, so this is tomorrow morning rather than 24 hours from now.",
				},
				{
					Title:  "Reformat an incoming timestamp",
					Params: json.RawMessage(`{"format":"rfc1123"}`),
					Notes:  "Connect an ISO-8601 string or Unix seconds into 'in'; it's parsed then re-rendered.",
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
					"locale": {
						"type":"string",
						"title":"Language",
						"default":"",
						"enum":["","en","sv"],
						"enumNames":["Follow the flow's language","English","Svenska"],
						"description":"Which language day and month names are written in — Thursday/August, torsdag/augusti. Leave it on the flow's language (Settings → General) unless this one step should differ. Only names are affected: numbers, and the ISO/Unix/email formats, are the same in every language."
					},
					"add":    {"type":"string","title":"Offset","description":"Shift the time by an offset, e.g. \"3d\", \"-2h30m\", \"1w\" — \"1d\" is tomorrow. Units: w (weeks), d (days), h, m, s. Empty = no shift."},
					"at":     {"type":"string","title":"At time of day","description":"Set the clock to this time of day, e.g. \"09:00\" or \"17:30:15\". Applied after the offset and in the output timezone, so Offset \"1d\" with At \"09:00\" is tomorrow morning rather than 24 hours from now. Empty keeps the time it already had."},
					"tz": {
						"type":"string",
						"title":"Timezone",
						"format":"timezone",
						"default":"UTC",
						"description":"Which timezone the output is written in — it decides the date at midnight, which weekday that is, and the hour on the clock. Any IANA name: search the list, or type one (\"Europe/Stockholm\", \"Pacific/Auckland\"). \"Local\" is the server's own zone, which on a hosted instance is not yours."
					},
					"format": {
						"type":"string",
						"title":"Format",
						"default":"iso",
						"enum":["iso","date","datetime","time24","time12","weekday","weekday_short","unix","unixms","rfc1123","custom"],
						"enumNames":[
							"ISO-8601 (2026-08-27T14:05:09Z)",
							"Date (2026-08-27)",
							"Date and time (2026-08-27 14:05:09)",
							"Time, 24-hour (14:05:09)",
							"Time, 12-hour (2:05:09 PM)",
							"Weekday (Thursday)",
							"Weekday, short (Thu)",
							"Unix seconds",
							"Unix milliseconds",
							"Email/HTTP (Thu, 27 Aug 2026 14:05:09 UTC)",
							"Custom…"
						],
						"description":"How the date is written on the 'out' port. Pick Custom to write your own — a field for it appears below."
					},
					"custom_format": {
						"type":"string",
						"title":"Custom format",
						"x_visible_when":{"format":"custom"},
						"description":"Build it from tokens: YYYY (2026) YY (26) MM (08) M (8) DD (27) D (27) HH (14) mm (05) ss (09), MMM/MMMM for a month name (Aug/August), ddd/dddd for a weekday (Thu/Thursday), hh with A for a 12-hour clock (02 PM), Z for the zone offset. Everything else — slashes, dots, spaces — is kept as typed, and literal words go in square brackets: \"[week of] D MMM\" → \"week of 27 Aug\". Tokens are case-sensitive (MM is the month, mm the minute); an unknown one fails the step rather than being printed as-is."
					}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeDate,
	})
}

// executeDate resolves the base time (the 'in' input, or now when unwired),
// applies the offset, timezone and time-of-day, then renders the result per
// 'format'.
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

	// Timezone first, because everything below is about the CALENDAR — which
	// day it is, which weekday that day falls on, what the clock reads — and
	// all three are answers only a timezone can give. Late on a Monday
	// evening UTC it is already Tuesday in Sydney, so snapping to "the next
	// Tuesday" from the UTC date would land a week off.
	loc, err := loadLocation(params.StringDefault(job.Params, "tz", ""))
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	t := base.In(loc)

	// Offset. Applied to the absolute instant, so a "1d" that crosses a
	// daylight-saving boundary moves 24 hours rather than one calendar day —
	// which is why "at" exists, and why it runs after this.
	if add := strings.TrimSpace(params.StringDefault(job.Params, "add", "")); add != "" {
		d, aErr := parseOffset(add)
		if aErr != nil {
			return params.Err(job, "bad_param", aErr.Error()), nil
		}
		t = t.Add(d)
	}

	// Time of day, applied AFTER the offset and IN the output timezone: with
	// an offset of "1d", "at 09:00" has to mean nine o'clock where the reader
	// is, not nine UTC re-expressed as some other hour. Without this the
	// offset alone can only say "24 hours from now", which lands at whatever
	// time the flow happened to run — fine for a date, wrong for a deadline.
	if at := strings.TrimSpace(params.StringDefault(job.Params, "at", "")); at != "" {
		hour, min, sec, cErr := parseClock(at)
		if cErr != nil {
			return params.Err(job, "bad_param", cErr.Error()), nil
		}
		t = time.Date(t.Year(), t.Month(), t.Day(), hour, min, sec, 0, loc)
	}

	// Render. The language is the step's own choice when it made one, the
	// flow's otherwise (Graph.Language, carried on the job) — so a flow set to
	// Swedish writes Swedish dates without touching every date step, and a
	// single step can still be pinned when one message needs another language.
	names := datenames.For(localeFor(job))
	out, err := renderFormat(t, job, names)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

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
// empty string is a zero offset. Shared with the relative time windows on
// steps like Google Calendar's, so "3d" means the same thing everywhere.
func parseOffset(s string) (time.Duration, error) {
	return reltime.ParseOffset(s)
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

// renderFormat resolves the Format field and renders t. Three cases, tried in
// this order:
//
//	a named format → its preset rendering
//	"custom"       → the Custom format field, in the token vocabulary
//	anything else  → a format typed into `format` itself, which is what this
//	                 drop took before Format became a dropdown; saved graphs
//	                 still carry Go reference layouts there, so they keep
//	                 working (see renderLegacyFormat).
func localeFor(job core.Job) string {
	if l := strings.TrimSpace(params.StringDefault(job.Params, "locale", "")); l != "" {
		return l
	}
	return job.Language
}

func renderFormat(t time.Time, job core.Job, names datenames.Names) (string, error) {
	format := strings.TrimSpace(params.StringDefault(job.Params, "format", "iso"))
	if strings.EqualFold(format, "custom") {
		custom := strings.TrimSpace(params.StringDefault(job.Params, "custom_format", ""))
		if custom == "" {
			return "", fmt.Errorf("Format is Custom but the Custom format field is empty — write one (e.g. \"DD/MM/YYYY\") or pick a named format")
		}
		return renderCustom(t, custom, names)
	}
	if out, ok := renderPreset(t, format, names); ok {
		return out, nil
	}
	return renderLegacyFormat(t, format, names)
}

// renderPreset formats t per a named format, reporting false for a name it
// doesn't know so the caller can treat the value as a format string.
func renderPreset(t time.Time, format string, names datenames.Names) (string, bool) {
	switch strings.ToLower(format) {
	case "", "iso", "rfc3339":
		return t.Format(time.RFC3339), true
	case "date":
		return t.Format("2006-01-02"), true
	case "datetime":
		return t.Format("2006-01-02 15:04:05"), true
	case "time24":
		return t.Format("15:04:05"), true
	case "time12":
		return t.Format("3:04:05 PM"), true
	// The day's NAME, not its number — "Thursday", "torsdag". Available as
	// ddd/dddd in a custom format too, but a named option is the discoverable
	// form: wanting the weekday out of a date is a common enough ask that it
	// should not require learning the token vocabulary first.
	//
	// These two and the name tokens are the ONLY localized formats. iso, unix,
	// unixms and rfc1123 are wire formats read by machines — RFC 1123 mandates
	// English abbreviations, so a Swedish "tors, 27 aug" in a Date: header is
	// a malformed header rather than a translation.
	case "weekday":
		return names.Days[int(t.Weekday())], true
	case "weekday_short":
		return names.DaysShort[int(t.Weekday())], true
	// "time" and "kitchen" are what the 24- and 12-hour options were called
	// before they were a pair, and saved graphs still carry them. Kept
	// renderable, and out of the dropdown: the two names said nothing about
	// each other, which is what sent people to a custom format to get a
	// 12-hour clock the step already had.
	case "time":
		return t.Format("15:04:05"), true
	case "kitchen":
		return t.Format(time.Kitchen), true
	case "unix":
		return strconv.FormatInt(t.Unix(), 10), true
	case "unixms":
		return strconv.FormatInt(t.UnixMilli(), 10), true
	case "rfc1123":
		return t.Format(time.RFC1123), true
	}
	return "", false
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
