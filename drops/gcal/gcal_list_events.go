package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gcal_list_events",
			Version:     "1.0",
			Label:       "Google Calendar",
			Subtitle:    "List events",
			Summary:     "List events from a Google Calendar in a time window.",
			Description: "List events from a Google Calendar. Optionally bound by a time window (time_min/time_max, RFC3339) and a text query. Recurring events are expanded into single instances and returned in start-time order. Each event becomes an object with id, summary, start/end, status and attendees.",
			Integration: "Google Calendar",
			Category:    "network",
			Icon:        "calendar",
			BrandLogo:   "/brands/google-calendar.svg",
			Color:       "#4285F4",
			Provider:    "internal",
			Tags:        []string{"calendar", "google", "events", "list"},
			Examples: []core.ParamsExample{
				{Title: "Upcoming events on the primary calendar", Params: json.RawMessage(`{"account":"default","calendar_id":"primary","limit":50}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — calendar.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Optional: wire a calendar id in to override the param, so a
				// reference can be threaded from an upstream step.
				{Port: "calendar_id", Label: "Calendar ID", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "events", Label: "Events", MIME: []string{"application/json"}},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"calendar_id":{"type":"string","format":"google-calendar","title":"Calendar","default":"primary","description":"The calendar to read — pick from your account's calendars, or 'primary' for your own."},
					"time_min":{"type":"string","title":"Start of window","examples":["2026-06-16T00:00:00Z"],"description":"Only events ending at or after this time (RFC3339). Leave blank for no lower bound."},
					"time_max":{"type":"string","title":"End of window","examples":["2026-06-23T00:00:00Z"],"description":"Only events starting before this time (RFC3339). Leave blank for no upper bound."},
					"q":{"type":"string","title":"Search text","description":"Free-text search over event fields. Leave blank to match all."},
					"limit":{"type":"integer","title":"Max events","default":250,"minimum":1,"maximum":2500,"description":"Upper bound on events returned."},
					"single_events":{"type":"boolean","title":"Expand recurring events","default":true,"description":"Expand recurring events into individual instances."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeListEvents,
	})
}

func executeListEvents(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	events, err := ListEvents(ctx, job)
	if err != nil {
		return params.Err(job, "gcal_error", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"events": {MIME: "application/json", Inline: events},
			"count":  {MIME: "text/plain", Inline: strconv.Itoa(len(events))},
		},
	}, nil
}

// resolveCalendarID prefers a wired 'calendar_id' input port over the param, so
// a calendar reference can be threaded in from an upstream step. Empty input
// falls back to the param (which defaults to "primary").
func resolveCalendarID(job core.Job) string {
	if in, ok := job.Input["calendar_id"]; ok && in.Inline != nil {
		switch v := in.Inline.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case []byte:
			if s := strings.TrimSpace(string(v)); s != "" {
				return s
			}
		}
	}
	return calendarID(job)
}

// ListEvents fetches and normalizes events for the chosen calendar and window.
// Exported so the daemon (e.g. a future poll trigger or resource provider) can
// reuse the exact read instead of reimplementing the Google call.
func ListEvents(ctx context.Context, job core.Job) ([]map[string]any, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}
	single := params.BoolDefault(job.Params, "single_events", true)

	q := url.Values{}
	q.Set("maxResults", strconv.Itoa(params.IntDefault(job.Params, "limit", 250)))
	q.Set("singleEvents", strconv.FormatBool(single))
	// orderBy=startTime is only valid when recurring events are expanded;
	// otherwise the API rejects the combination, so fall back to its default.
	if single {
		q.Set("orderBy", "startTime")
	}
	if v := strings.TrimSpace(params.StringDefault(job.Params, "time_min", "")); v != "" {
		q.Set("timeMin", v)
	}
	if v := strings.TrimSpace(params.StringDefault(job.Params, "time_max", "")); v != "" {
		q.Set("timeMax", v)
	}
	if v := strings.TrimSpace(params.StringDefault(job.Params, "q", "")); v != "" {
		q.Set("q", v)
	}

	endpoint := calBaseURL(job) + "/calendars/" + url.PathEscape(resolveCalendarID(job)) + "/events?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", calErr(body))
	}

	var parsed struct {
		Items []rawEvent `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("events.list decode: %w", err)
	}
	out := make([]map[string]any, 0, len(parsed.Items))
	for _, it := range parsed.Items {
		out = append(out, it.normalize())
	}
	return out, nil
}

// rawEvent is the slice of the Calendar event resource the drop surfaces.
type rawEvent struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	HTMLLink    string     `json:"htmlLink"`
	Summary     string     `json:"summary"`
	Description string     `json:"description"`
	Location    string     `json:"location"`
	Start       eventTime  `json:"start"`
	End         eventTime  `json:"end"`
	Attendees   []attendee `json:"attendees"`
}

type eventTime struct {
	DateTime string `json:"dateTime"`
	Date     string `json:"date"`
}

type attendee struct {
	Email string `json:"email"`
}

// when returns the timed instant if present, else the all-day date.
func (t eventTime) when() string {
	if t.DateTime != "" {
		return t.DateTime
	}
	return t.Date
}

func (e rawEvent) normalize() map[string]any {
	emails := make([]string, 0, len(e.Attendees))
	for _, a := range e.Attendees {
		if a.Email != "" {
			emails = append(emails, a.Email)
		}
	}
	return map[string]any{
		"id":          e.ID,
		"status":      e.Status,
		"summary":     e.Summary,
		"description": e.Description,
		"location":    e.Location,
		"html_link":   e.HTMLLink,
		"start":       e.Start.when(),
		"end":         e.End.when(),
		// all_day is true when the event carries a date but no dateTime.
		"all_day":   e.Start.Date != "" && e.Start.DateTime == "",
		"attendees": emails,
	}
}
