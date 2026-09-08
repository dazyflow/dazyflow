// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/pollstate"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "google_form_trigger",
			Version:     "1.0",
			Label:       "Google Forms",
			Subtitle:    "New responses",
			Summary:     "Fires when a Google Form gets new responses, emitting each answer keyed by its question title.",
			Description: "Watches a Google Form and fires when new responses arrive (each response exactly once). The first check after you publish records what is already there and fires nothing, so turning the flow on doesn't process a form's whole back catalogue. `responses` is a list of objects keyed by question title — connect it straight into a Sheets append. Each response also carries `email` (the respondent's address) when the form collects email addresses, so you can reply to them. When a check finds nothing new, the rest of the flow is skipped. Publish the flow so it runs automatically on the schedule below — pressing Run only checks once, for testing.",
			Integration: "Google Forms",
			Category:    "trigger",
			Icon:        "clipboard-list",
			BrandLogo:   "/brands/forms.svg",
			Color:       "#7248B9",
			Provider:    "internal",
			Tags:        []string{"google", "forms", "trigger", "poll"},
			Examples: []core.ParamsExample{
				{
					Title:  "Every 5 minutes",
					Params: json.RawMessage(`{"account":"default","form_id":"REPLACE_WITH_YOUR_FORM_ID","interval_seconds":300}`),
					Notes:  "Checks the form on this interval; new submissions come out on the 'New responses' output.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — Forms responses + body (read)."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "responses", Label: "New responses", MIME: []string{"application/json"}},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2"`)},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2026-02-12T08:12:04Z"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"form_id":{"type":"string","format":"google-form","title":"Form","description":"The Google Form to watch for new responses."},
					"interval_seconds":{
						"type":"integer",
						"title":"Check every",
						"format":"duration-seconds",
						"minimum":1,
						"maximum":31622400,
						"default":300,
						"description":"How often to check for new responses once the flow is published. Leave blank to only check when you press Run (for testing)."
					},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["form_id"]
			}`),
			// A fire is a discrete poll of the response stream; rerunning
			// re-reads from the stored cursor rather than re-deriving a
			// specific past fire, so it's not idempotent in the retry sense.
			Idempotent: false,
		},
		Execute: executeGoogleFormTrigger,
	})
}

func executeGoogleFormTrigger(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	formID := extractFormID(params.StringDefault(job.Params, "form_id", ""))
	if formID == "" {
		return params.Err(job, "bad_param", "'form_id' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	timeout := params.IntDefault(job.Params, "timeout_ms", 15000)

	titles, err := fetchTitles(ctx, job, formID, token, timeout)
	if err != nil {
		return params.Err(job, "forms_error", err.Error()), nil
	}

	cursorName := fmt.Sprintf("cursor.gform.%s.%s", job.GraphID, job.NodeID)
	last, rerr := cursor.Read(ctx, job.Tenant, cursorName)
	if rerr != nil {
		// Without the watermark this run cannot tell new responses from ones
		// already emitted, and guessing "first run" would re-baseline over it
		// — silently discarding every response submitted since the last poll.
		return cursor.FailRead(job, rerr), nil
	}

	fresh, newCursor, err := fetchNewResponses(ctx, job, formID, token, timeout, last)
	if err != nil {
		return params.Err(job, "forms_error", err.Error()), nil
	}

	// First fire after publishing: record where the form is up to and emit
	// NOTHING, so the flow starts watching from now.
	//
	// This step used to be the odd one out. With no stored watermark every
	// response counts as new (see newerThan), so publishing a flow against a
	// form that already had 500 responses fired all 500 into a step that acts
	// on each one — 500 emails, 500 rows, 500 whatever. Every sibling watcher
	// baselines silently for exactly this reason (gmail_search_messages,
	// imap_search_messages, rss, sftp_list_files,
	// homeassistant_state_changed, ticketmaster_on_new_event), and two of
	// them carry comments claiming they mirror THIS one, which was the wrong
	// way round.
	//
	// It also removes a way to lose the lot: emitting a backlog and then
	// failing to record it meant re-emitting the same backlog on every fire,
	// for ever, against a step whose contract is "each response exactly once".
	if last == "" {
		if newCursor != "" {
			if werr := cursor.Write(ctx, job.Tenant, cursorName, newCursor); werr != nil {
				return cursor.FailBaseline(job, werr), nil
			}
		}
		params.EmitProgress(progress, job, 1, fmt.Sprintf(
			"baseline: %d existing response(s) recorded, none emitted — now watching for new ones",
			len(fresh)))
		pollstate.Report(ctx, job, false)
		return core.Result{JobID: job.ID, Status: core.StatusOK, Output: emitOutput(nil)}, nil
	}

	out := make([]map[string]any, 0, len(fresh))
	for _, r := range fresh {
		out = append(out, mapAnswers(r, titles))
	}

	pollstate.Report(ctx, job, len(out) > 0)

	// Advance the cursor only when it actually moved. Best-effort from here on:
	// the responses have gone downstream, so a failed write means at worst the
	// next fire re-emits this batch, which is at-least-once and the safe
	// direction. The baseline write is the one that must not be ignored, and
	// it is handled in the first-fire branch above.
	if newCursor != "" && newCursor != last {
		_ = cursor.Write(ctx, job.Tenant, cursorName, newCursor)
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: emitOutput(out),
	}, nil
}

func emitOutput(out []map[string]any) map[string]core.Ref {
	if len(out) == 0 {
		return map[string]core.Ref{}
	}
	return map[string]core.Ref{
		"responses": {MIME: "application/json", Inline: out},
		"count":     {MIME: "text/plain", Inline: strconv.Itoa(len(out))},
		"fired_at":  {MIME: "text/plain", Inline: nowRFC3339()},
	}
}

// FieldNames returns the field names a response from this form carries —
// the (sanitized, deduped, sorted) question titles plus the always-present
// structural keys (responseId, submittedTime). It's the live counterpart
// the daemon's row-source registry uses to populate the Sheets mapping
// suggestions: a real Forms API call (forms.get) resolves the titles, so a
// user mapping a form to a sheet sees the actual question labels. Reads
// form_id/account/timeout_ms from job.Params; the tenant rides on ctx for
// the OAuth lookup.
func FieldNames(ctx context.Context, job core.Job) ([]string, error) {
	formID := extractFormID(params.StringDefault(job.Params, "form_id", ""))
	if formID == "" {
		return nil, fmt.Errorf("'form_id' is required")
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}
	titles, err := fetchTitles(ctx, job, formID, token, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(titles)+2)
	for _, title := range titles {
		s := sanitizeTitle(title)
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	// Structural keys every response carries, appended after the titles.
	// `email` is only populated when the form collects email addresses, but we
	// always surface it as a hint so a reply/email step can map it.
	return append(out, "email", "responseId", "submittedTime"), nil
}

func fetchTitles(ctx context.Context, job core.Job, formID, token string, timeoutMS int) (map[string]string, error) {
	if titles, ok := cachedTitles(formID); ok {
		return titles, nil
	}
	endpoint := formsBaseURL(job) + "/forms/" + url.PathEscape(formID)
	status, body, err := googleGet(ctx, endpoint, token, timeoutMS)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("forms.get: %s", formsErr(body))
	}
	var f formStructure
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("forms.get decode: %w", err)
	}
	titles := f.titleMap()
	storeTitles(formID, titles)
	return titles, nil
}

func fetchNewResponses(ctx context.Context, job core.Job, formID, token string, timeoutMS int, cursor string) ([]formResponse, string, error) {
	base := formsBaseURL(job) + "/forms/" + url.PathEscape(formID) + "/responses"
	newCursor := cursor
	var fresh []formResponse
	pageToken := ""
	for {
		q := url.Values{}
		q.Set("pageSize", "200")
		if cursor != "" {
			// Forms filter grammar; client-side re-filter below guards the
			// boundary since the API filter's precision is coarse.
			q.Set("filter", "timestamp > "+cursor)
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		status, body, err := googleGet(ctx, base+"?"+q.Encode(), token, timeoutMS)
		if err != nil {
			return nil, cursor, err
		}
		if status < 200 || status >= 300 {
			return nil, cursor, fmt.Errorf("forms.responses.list: %s", formsErr(body))
		}
		var page responsesList
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, cursor, fmt.Errorf("forms.responses.list decode: %w", err)
		}
		for _, r := range page.Responses {
			if newerThan(r.LastSubmittedTime, cursor) {
				fresh = append(fresh, r)
				newCursor = maxTime(newCursor, r.LastSubmittedTime)
			}
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return fresh, newCursor, nil
}
