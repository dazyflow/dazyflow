// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package caldav holds the drops that read and write a calendar over CalDAV —
// the vendor-neutral counterpart to the Google Calendar steps. Fastmail,
// mailbox.org, iCloud, Nextcloud and a Radicale box of your own all speak it,
// so the reminder and booking flows stop being Google-only.
//
// The record shape deliberately matches gcal_list_events', so a flow built on
// one becomes a flow on the other by swapping the step — the same reasoning
// behind the Mailbox drops matching Gmail's.
package caldav

import (
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/reltime"
	"github.com/dazyflow/dazyflow/internal/caldavutil"
)

// integration is the label every drop here shares — the name of the page a
// tenant configures once, and the key its stored connection hangs off.
const integration = "Calendar"

// brandColor is shared by every step in the app so the cards read as one
// group on the canvas.
const brandColor = "#0891b2"

// connectionFields is the calendar account, configured once on the
// integration page and injected into every node's params at run time
// (injectConnectionDefaults) — so flows carry only the per-event fields, and
// the password never lands in a graph.
//
// Every drop in the integration MUST declare this same slice: the connection
// UI takes the fields from whichever drop it finds first, so a drop declaring
// a subset would render a page missing whatever it left out.
func connectionFields() []core.ConnectionField {
	return []core.ConnectionField{
		{Key: "url", Label: "Calendar server", Required: true, Placeholder: "https://caldav.fastmail.com/", Help: "The CalDAV address your provider publishes. Nextcloud looks like https://cloud.example.com/remote.php/dav/."},
		{Key: "username", Label: "Username", Required: true, Placeholder: "usually your email address"},
		{Key: "password", Label: "Password", Secret: true, Required: true, Help: "Your calendar password — or, on a provider with two-factor sign-in (Fastmail, iCloud), an app password generated for this."},
		{Key: "calendar", Label: "Calendar", Placeholder: "Work", Help: "Which calendar to use when the account has several. Leave blank if there's only one; Test connection will list them if there are more."},
	}
}

// configFromJob assembles the calendar connection from the params the engine
// injected. `calendar` is declared as a param as well as a connection field,
// so a step can point at another calendar while everything else comes from
// the connection — injectConnectionDefaults leaves an author's per-step value
// alone.
func configFromJob(job core.Job) (caldavutil.Config, error) {
	raw := map[string]string{
		"url":      params.StringDefault(job.Params, "url", ""),
		"username": params.StringDefault(job.Params, "username", ""),
		"password": params.StringDefault(job.Params, "password", ""),
		"calendar": params.StringDefault(job.Params, "calendar", ""),
	}
	if strings.TrimSpace(raw["url"]) == "" {
		return caldavutil.Config{}, fmt.Errorf("no calendar connected — set one up on the Calendar integration page")
	}
	return caldavutil.ConfigFromConn(raw)
}

// location resolves the step's timezone. Empty means UTC, matching the
// Google Calendar steps — the day boundaries of "today"/"tomorrow" are taken
// in it, so a nightly schedule always picks up exactly the next day.
func location(job core.Job) (*time.Location, error) {
	tz := strings.TrimSpace(params.StringDefault(job.Params, "tz", ""))
	if tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("%q isn't a timezone name — use an IANA one like \"Europe/Stockholm\"", tz)
	}
	return loc, nil
}

// resolveWindowEnd turns one end of a time window into an instant.
//
// It accepts the same relative forms the Google Calendar steps do — now,
// today, tomorrow, +3d, tomorrow+9h — so a window can be written once and
// moved between the two steps.
//
// It also accepts what the Date & time step emits, which is the most likely
// thing wired into these ports: ISO/RFC3339 (its default), a plain date, the
// "2026-06-16 09:00[:00]" forms, and Unix seconds. Those are exactly
// reltime's input layouts, which the Date step shares — the two were built to
// interoperate. What does NOT flow in is a Date step set to render for a
// HUMAN: a weekday name, the email/HTTP format, a 12-hour clock, or a custom
// pattern like "DD/MM/YYYY". Those are display strings, and reltime says so
// by name rather than guessing at a locale's date order.
func resolveWindowEnd(job core.Job, port, param string, loc *time.Location, now time.Time) (time.Time, bool, error) {
	raw, ok := params.TextInputOr(job, port, params.StringDefault(job.Params, param, ""))
	if !ok {
		return time.Time{}, false, fmt.Errorf("input port %q must be text", port)
	}
	if raw = strings.TrimSpace(raw); raw == "" {
		return time.Time{}, false, nil
	}
	// reltime's own error already names the value and lists the forms that
	// work, so it is returned as-is: wrapping it produced "couldn't read
	// \"Thursday\" as a time: couldn't read \"Thursday\" as a time — …".
	stamp, err := reltime.ResolveRFC3339(raw, loc, now)
	if err != nil {
		return time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		// Unreachable in practice — ResolveRFC3339 formats what it returns —
		// but a silent zero time would book an event in year 1.
		return time.Time{}, false, fmt.Errorf("couldn't read %q as a time: %w", raw, err)
	}
	return t, true, nil
}

// eventRecord reduces one calendar event to the friendly record a flow works
// with.
//
// Deliberately the same shape gcal_list_events emits — {id, summary,
// description, location, start, end, status, attendees} — so the idioms built
// on that carry over unchanged, and a reminder flow becomes provider-neutral
// by swapping the step.
func eventRecord(event *ical.Event, loc *time.Location) map[string]any {
	text := func(name string) string {
		v, err := event.Props.Text(name)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(v)
	}
	rec := map[string]any{
		"id":          text(ical.PropUID),
		"summary":     text(ical.PropSummary),
		"description": text(ical.PropDescription),
		"location":    text(ical.PropLocation),
		"status":      strings.ToLower(text(ical.PropStatus)),
		"start":       "",
		"end":         "",
		"attendees":   attendees(event),
	}
	if start, err := event.DateTimeStart(loc); err == nil && !start.IsZero() {
		rec["start"] = start.Format(time.RFC3339)
	}
	if end, err := event.DateTimeEnd(loc); err == nil && !end.IsZero() {
		rec["end"] = end.Format(time.RFC3339)
	}
	return rec
}

// attendees lists the invitees' addresses, stripped of the "mailto:" prefix
// iCalendar wraps them in — a flow wiring these into an email's To field
// wants addresses, not URIs.
func attendees(event *ical.Event) []string {
	out := []string{}
	for _, prop := range event.Props.Values(ical.PropAttendee) {
		addr := strings.TrimSpace(prop.Value)
		if addr == "" {
			continue
		}
		if rest, ok := cutPrefixFold(addr, "mailto:"); ok {
			addr = rest
		}

		out = append(out, addr)
	}
	return out
}

// cutPrefixFold is strings.CutPrefix, case-insensitively — "MAILTO:" is as
// valid as "mailto:" in iCalendar, and both turn up.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}
