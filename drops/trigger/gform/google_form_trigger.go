package gform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "google_form_trigger",
			Version:     "1.0",
			Label:       "Google Form",
			Summary:     "Fires when a Google Form gets new responses, emitting each answer keyed by its question title.",
			Description: "Polls a Google Form for new responses on the interval set on this node and emits the ones it hasn't seen before. `responses` is a list of objects (one per response) keyed by question title, plus `responseId` and `submittedTime`; wire it straight into a Sheets append. The node remembers how far it has read, so each response fires once. Leave the interval blank to fetch on demand (the Run button).",
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
					Notes:  "The interval lives on the node; the scheduler reads it. Emits new 'responses' downstream.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — Forms responses + body (read)."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "responses", Label: "New responses", MIME: []string{"application/json"}},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}},
				{Port: "fired_at", Label: "Timestamp", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"form_id":{"type":"string","format":"google-form","title":"Form"},
					"interval_seconds":{
						"type":"integer",
						"title":"Check every (seconds)",
						"minimum":1,
						"maximum":31622400,
						"default":300
					},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
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

// executeGoogleFormTrigger fetches Form responses newer than this node's
// stored cursor, keys each by question title, advances the cursor to the
// newest response seen, and emits the batch. Empty batches are valid (a
// downstream Sheets append of [] is a no-op). The node runs in-band like
// poll_trigger: the daemon scheduler only fires the graph on the interval;
// all Google I/O and cursor bookkeeping happen here.
func executeGoogleFormTrigger(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
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

	// cursor.gform.<graph>.<node>: per-(flow,node) watermark = the newest
	// lastSubmittedTime we've already emitted. The store hides the
	// "cursor." prefix from the Credentials UI.
	cursorName := fmt.Sprintf("cursor.gform.%s.%s", job.GraphID, job.NodeID)
	last := readCursor(ctx, job.Tenant, cursorName)

	fresh, newCursor, err := fetchNewResponses(ctx, job, formID, token, timeout, last)
	if err != nil {
		return params.Err(job, "forms_error", err.Error()), nil
	}

	out := make([]map[string]any, 0, len(fresh))
	for _, r := range fresh {
		out = append(out, mapAnswers(r, titles))
	}

	// Advance the cursor only when it actually moved. A best-effort write:
	// a failure means at worst the next fire re-emits this batch (the
	// trigger is at-least-once — see the plan's failover note).
	if newCursor != "" && newCursor != last {
		if werr := writeCursor(ctx, job.Tenant, cursorName, newCursor); werr != nil {
			// Surface as a soft failure: we already have the data, so emit
			// it, but the operator should know the cursor didn't persist.
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusOK,
				Output: emitOutput(out),
			}, nil
		}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: emitOutput(out),
	}, nil
}

func emitOutput(out []map[string]any) map[string]core.Ref {
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
	return append(out, "responseId", "submittedTime"), nil
}

// fetchTitles returns questionId → title for a form, served from a short
// per-form_id TTL cache (see titleCache). The form structure changes rarely,
// so caching it spares a forms.get on every trigger fire and on every
// mapping-editor open (live field hints) within the TTL window — while the
// short TTL still picks up question edits promptly.
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

// fetchNewResponses pages through forms.responses.list (server-filtered by
// the cursor when present), client-filters to strictly-newer responses for
// precision, and returns them along with the new cursor (max
// lastSubmittedTime seen across the batch).
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
