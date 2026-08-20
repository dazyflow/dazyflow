// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gmail_search_messages",
			Version:     "1.0",
			Label:       "Gmail",
			Subtitle:    "Search emails",
			Summary:     "Find emails in the connected mailbox, using the same search you'd type in Gmail.",
			Description: "Find emails in the connected mailbox. The search works exactly like Gmail's own search box (e.g. 'from:boss@company.com is:unread' or 'newer_than:1d'). Each match comes out as a real email — date, sender, subject and body — ready to log to a sheet, loop over with For each, or wire into Gmail · Read email to take the newest one.",
			Integration: "Gmail",
			Category:    "network",
			Icon:        "search",
			BrandLogo:   "/brands/gmail.svg",
			Color:       "#D14836",
			Provider:    "internal",
			Tags:        []string{"gmail", "email", "search", "list"},
			Examples: []core.ParamsExample{
				{Title: "Unread from the last day", Params: json.RawMessage(`{"account":"default","query":"newer_than:1d is:unread","max_results":20}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — gmail.readonly scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Editable on the card (inline pin editor — the port name
				// matches the string param) and wireable from upstream; a
				// wired value overrides the param.
				{Port: "query", Label: "Search", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				// Matching emails is a list of real email records — {date,
				// from, subject, body, id, threadId} — expanded from Gmail's
				// ID stubs at run time. next_page_token is still EMITTED for
				// API callers that paginate by hand, but not declared:
				// pagination is dev plumbing a flow can't loop on anyway.
				{Port: "messages", Label: "Matching emails", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"query":{"type":"string","title":"Search","examples":["from:boss@company.com is:unread"],"description":"Works exactly like Gmail's search box, e.g. 'is:unread', 'newer_than:1d', 'from:someone@example.com'."},
					"only_new":{"type":"boolean","title":"Only new since last run","default":false,"description":"When on, each run emits only emails that arrived since the previous run — nothing on the first run (it just remembers the newest email as the starting point). Turn this on when a published, polling flow acts on each match (e.g. sends a reply), so it doesn't re-process the same emails on every poll or blast the whole mailbox on publish. Leave off for ad-hoc searches that should return every match."},
					"max_results":{"type":"integer","title":"Max emails","default":50,"minimum":1,"maximum":500},
					"page_token":{"type":"string","title":"Page token","x_advanced":true,"description":"Pagination token from a prior run's next_page_token output (advanced)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeGmailSearch,
	})
}

func executeGmailSearch(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	q := url.Values{}
	q.Set("maxResults", strconv.Itoa(params.IntDefault(job.Params, "max_results", 50)))
	// The Search input pin overrides the param when wired (same pattern as
	// gmail send's to/subject/body).
	queryParam, _ := params.StringOpt(job.Params, "query")
	query, ok := params.TextInputOr(job, "query", queryParam)
	if !ok {
		return params.Err(job, "bad_input", "input port 'query' must be text"), nil
	}
	if query != "" {
		q.Set("q", query)
	}
	if pt, _ := params.StringOpt(job.Params, "page_token"); pt != "" {
		q.Set("pageToken", pt)
	}

	endpoint := baseURL(job) + "/users/me/messages?" + q.Encode()
	status, body, err := gmailDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "gmail_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "gmail_error", extractGmailError(body)), nil
	}

	var parsed struct {
		Messages      []any  `json:"messages"`
		NextPageToken string `json:"nextPageToken"`
	}
	_ = json.Unmarshal(body, &parsed)
	if parsed.Messages == nil {
		parsed.Messages = []any{}
	}

	// Gmail's search API returns only {id, threadId} stubs. Expand every
	// match into a real email record (date / from / subject / body) with
	// bounded concurrency, so downstream steps work with emails, never IDs.
	// A failed fetch degrades that one entry to its stub rather than
	// failing the whole search. dates[i] holds the message's internalDate
	// (epoch ms) — Gmail's authoritative receive time — for the watermark.
	timeout := params.IntDefault(job.Params, "timeout_ms", 15000)
	msgs := make([]any, len(parsed.Messages))
	dates := make([]string, len(parsed.Messages))
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i, m := range parsed.Messages {
		stub, _ := m.(map[string]any)
		id := str(stub["id"])
		msgs[i] = map[string]any{"id": id, "threadId": str(stub["threadId"])}
		if id == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			// A panic in here would take down the whole daemon, not just this
			// job: the engine's recover wraps Execute on the calling
			// goroutine and cannot see a panic raised on one we spawned. Drop
			// the message instead — the caller already tolerates a stub-only
			// entry for a fetch that failed.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("gmail_search_messages: recovered while hydrating message %s: %v", id, r)
				}
			}()
			ep := baseURL(job) + "/users/me/messages/" + url.PathEscape(id) + "?format=full"
			st, b, ferr := gmailDo(ctx, "GET", ep, token, "", nil, timeout)
			if ferr != nil || st < 200 || st >= 300 {
				return
			}
			var raw map[string]any
			if json.Unmarshal(b, &raw) != nil {
				return
			}
			flat := flatten(raw)
			dates[i] = str(flat["internal_date_ms"])
			msgs[i] = friendlyMessage(flat)
		}(i, id)
	}
	wg.Wait()

	// Opt-in watermark: only emit emails newer than the newest one seen on a
	// previous run. Off by default so an ad-hoc search still returns every
	// match; on, it turns this into a safe poll source — a published flow
	// that acts on each match won't re-process the backlog every poll or
	// blast the whole mailbox the first time it fires after publish.
	if params.BoolDefault(job.Params, "only_new", false) {
		return emitOnlyNew(ctx, job, msgs, dates, parsed.NextPageToken), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"messages":        {MIME: "application/json", Inline: msgs},
			"next_page_token": {MIME: "text/plain", Inline: parsed.NextPageToken},
		},
	}, nil
}

// emitOnlyNew applies the per-(flow,node) watermark. It filters msgs to those
// strictly newer than the stored cursor (by internalDate, epoch ms), advances
// the cursor to the newest email seen, and emits the fresh batch.
//
// First run (empty cursor): baseline to the newest email present and emit
// NOTHING — the flow starts watching from "now", never replaying the existing
// mailbox. Mirrors google_form_trigger / homeassistant_state_changed.
//
// A nothing-new (or first) run emits no output ports, so downstream edges go
// dormant and the rest of the flow is skipped — an empty poll is a non-event.
// The cursor write is best-effort/at-least-once: a failed write means at worst
// the next run re-emits this batch, never a silent drop.
func emitOnlyNew(ctx context.Context, job core.Job, msgs []any, dates []string, nextPageToken string) core.Result {
	// cursor.gmail_search.<graph>.<node>: per-(flow,node) watermark = the
	// newest internalDate we've already emitted. The store hides the
	// "cursor." prefix from the Credentials UI.
	cursorName := fmt.Sprintf("cursor.gmail_search.%s.%s", job.GraphID, job.NodeID)
	last := readCursor(ctx, job.Tenant, cursorName)
	first := last == ""

	fresh := make([]any, 0, len(msgs))
	newCursor := last
	for i, m := range msgs {
		d := dates[i]
		if d == "" {
			continue // couldn't resolve a receive time (expansion failed) — skip
		}
		if newerMillis(d, newCursor) {
			newCursor = d
		}
		// On the first run we emit nothing; every match only advances the
		// baseline above.
		if !first && newerMillis(d, last) {
			fresh = append(fresh, m)
		}
	}

	if newCursor != "" && newCursor != last {
		_ = writeCursor(ctx, job.Tenant, cursorName, newCursor)
	}

	// Nothing new (or first-run baseline) → emit no ports, skipping downstream.
	if len(fresh) == 0 {
		return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"messages":        {MIME: "application/json", Inline: fresh},
			"next_page_token": {MIME: "text/plain", Inline: nextPageToken},
		},
	}
}

// newerMillis reports whether epoch-ms timestamp a is strictly after cursor.
// An empty cursor makes everything newer. Both are Gmail internalDate strings;
// parse failures fall back to a length-then-lexical compare, correct for the
// equal-width millisecond values Gmail returns.
func newerMillis(a, cursor string) bool {
	if cursor == "" {
		return true
	}
	ai, aerr := strconv.ParseInt(a, 10, 64)
	ci, cerr := strconv.ParseInt(cursor, 10, 64)
	if aerr == nil && cerr == nil {
		return ai > ci
	}
	if len(a) != len(cursor) {
		return len(a) > len(cursor)
	}
	return a > cursor
}
