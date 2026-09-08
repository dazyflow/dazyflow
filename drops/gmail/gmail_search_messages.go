// SPDX-FileCopyrightText: 2026 Angels' Ware
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
	"sync/atomic"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gmail_search_messages",
			Version:     "1.0",
			Label:       "Gmail",
			Subtitle:    "Search emails",
			Summary:     "Find emails in the connected mailbox, using the same search you'd type in Gmail.",
			Description: "Find emails in the connected mailbox. The search works exactly like Gmail's own search box (e.g. 'from:boss@company.com is:unread' or 'newer_than:1d'). Each match comes out as a real email — date, sender, subject and body — ready to log to a sheet, loop over with For each, or connect into Gmail · Read email to take the newest one.",
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
				{Port: "query", Label: "Search", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				// Matching emails is a list of real email records, expanded from Gmail's ID
				// stubs at run time. next_page_token is still EMITTED for API callers that
				// paginate by hand, but not declared: pagination is dev plumbing a flow cannot
				// loop on anyway.
				{Port: "messages", Label: "Matching emails", MIME: []string{"application/json"},
					Example: json.RawMessage(`[
						{"id":"18f2a9c4d1e0b7a3","threadId":"18f2a9c4d1e0b7a3","date":"Thu, 12 Feb 2026 09:12:04 +0100","from":"Fortnox <faktura@fortnox.se>","subject":"Faktura 4471","body":"Din faktura 4471 är nu tillgänglig."},
						{"id":"18f2a7b19c3d5e21","threadId":"18f2a7b19c3d5e21","date":"Thu, 12 Feb 2026 08:47:51 +0100","from":"Stripe <billing@stripe.com>","subject":"Your receipt #A82","body":"Thanks for your payment of 249.00 SEK."}
					]`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"query":{"type":"string","title":"Search","examples":["from:boss@company.com is:unread"],"description":"Works exactly like Gmail's search box, e.g. 'is:unread', 'newer_than:1d', 'from:someone@example.com'."},
					"only_new":{"type":"boolean","title":"Only new since last run","default":false,"description":"When on, each run emits only emails that arrived since the previous run — nothing on the first run (it just remembers the newest email as the starting point). Turn this on when a published, polling flow acts on each match (e.g. sends a reply), so it doesn't re-process the same emails on every poll or blast the whole mailbox on publish. Leave off for ad-hoc searches that should return every match."},
					"max_results":{"type":"integer","title":"Max emails","default":50,"minimum":1,"maximum":500,"description":"How many emails to bring back at most. An ad-hoc search returns the newest ones. With 'Only new since last run' on it works through the OLDEST waiting emails first, in the order they arrived, and the rest follow on the next polls — so a burst bigger than this is delayed, never skipped."},
					"page_token":{"type":"string","title":"Page token","x_advanced":true,"description":"Pagination token from a prior run's next_page_token output (advanced)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeGmailSearch,
	})
}

func executeGmailSearch(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	maxResults := params.IntDefault(job.Params, "max_results", 50)
	queryParam, _ := params.StringOpt(job.Params, "query")
	query, ok := params.TextInputOr(job, "query", queryParam)
	if !ok {
		return params.Err(job, "bad_input", "input port 'query' must be text"), nil
	}
	pageToken, _ := params.StringOpt(job.Params, "page_token")
	timeout := params.IntDefault(job.Params, "timeout_ms", 15000)

	// Poll mode needs the watermark before it can ask Gmail the right question, so
	// it has its own path. A hand-driven page_token stays on the single-page path:
	// the caller is paginating themselves and must keep getting what they asked for.
	if params.BoolDefault(job.Params, "only_new", false) && pageToken == "" {
		return pollNewMail(ctx, job, token, query, maxResults, timeout, progress)
	}

	q := url.Values{}
	q.Set("maxResults", strconv.Itoa(maxResults))
	if query != "" {
		q.Set("q", query)
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}

	stubs, nextPageToken, fail := listMessages(ctx, job, token, q, timeout)
	if fail != nil {
		return *fail, nil
	}
	parsed := struct {
		Messages      []any
		NextPageToken string
	}{Messages: stubs, NextPageToken: nextPageToken}

	msgs, dates, unresolvedN := hydrateAll(ctx, job, token, parsed.Messages, timeout)

	if params.BoolDefault(job.Params, "only_new", false) {
		name, last, fail := readWatermark(ctx, job)
		if fail != nil {
			return *fail, nil
		}
		return emitOnlyNew(ctx, job, name, last, msgs, dates,
			parsed.NextPageToken, unresolvedN, progress), nil
	}
	if n := unresolvedN; n > 0 {
		params.EmitProgress(progress, job, 1, fmt.Sprintf(
			"%d of %d email(s) could not be fetched and are listed by id only", n, len(msgs)))
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

const (
	// backlogPageSize: Gmail's ceiling is 500 and a list page carries only {id,
	// threadId}, so scanning wide is cheap — the per-message expansion is what
	// costs, and that stays capped at the user's Max emails.
	backlogPageSize = 500

	// maxBacklogPages: past it the poll refuses rather than guessing. See
	// pollNewMail.
	maxBacklogPages = 4

	// maxDrainRounds is a ceiling on wasted calls, not on progress: a slice that is
	// entirely stale means its emails were already emitted, and the window only steps
	// toward mail that has not been. Three is generous — it takes maxResults×3 emails
	// inside the watermark's own second to exhaust.
	maxDrainRounds = 3
)

// readWatermark's fail is non-nil when the position could not be determined, in
// which case the caller must stop without writing anything.
func readWatermark(ctx context.Context, job core.Job) (name, last string, fail *core.Result) {
	name = fmt.Sprintf("cursor.gmail_search.%s.%s", job.GraphID, job.NodeID)
	last, err := cursor.Read(ctx, job.Tenant, name)
	if err != nil {
		// Reading this as a first run would re-baseline to the newest message present
		// and mark everything since the last poll as handled — mail skipped for good, on
		// a run that reported success.
		res := cursor.FailRead(job, err)
		return name, "", &res
	}
	return name, last, nil
}

func listMessages(ctx context.Context, job core.Job, token string, q url.Values, timeoutMS int) ([]any, string, *core.Result) {
	endpoint := baseURL(job) + "/users/me/messages?" + q.Encode()
	status, body, err := gmailDo(ctx, "GET", endpoint, token, "", nil, timeoutMS)
	if err != nil {
		res := params.Err(job, "gmail_http_error", err.Error())
		return nil, "", &res
	}
	if status < 200 || status >= 300 {
		res := params.Err(job, "gmail_error", extractGmailError(body))
		return nil, "", &res
	}
	var parsed struct {
		Messages      []any  `json:"messages"`
		NextPageToken string `json:"nextPageToken"`
	}
	_ = json.Unmarshal(body, &parsed)
	if parsed.Messages == nil {
		parsed.Messages = []any{}
	}
	return parsed.Messages, parsed.NextPageToken, nil
}

// pollNewMail emits each email that arrived since the last run, exactly once,
// oldest first.
//
// It is its own path because a poll has to ask Gmail a different question from an
// ad-hoc search, and the old code asked the search's. messages.list returns the
// newest maxResults matches, so with a 50-email cap and 200 new emails the poll
// saw the newest 50 and advanced the watermark past them, putting the other 150
// permanently behind it — silently, on a green run. No watermark arithmetic fixes
// that, because the 150 are never in the response: the QUERY has to change.
//
// So `after:<watermark>` goes into the query, making the result set the backlog
// itself, and the backlog is scanned ids-only and drained from its OLDEST end,
// capped at maxResults. The watermark then advances only as far as the emails
// actually emitted. Same shape as sftp_list_files.
func pollNewMail(
	ctx context.Context,
	job core.Job,
	token, query string,
	maxResults, timeoutMS int,
	progress chan<- core.Progress,
) (core.Result, error) {
	name, last, fail := readWatermark(ctx, job)
	if fail != nil {
		return *fail, nil
	}

	q := url.Values{}
	q.Set("maxResults", strconv.Itoa(backlogPageSize))
	q.Set("q", backlogQuery(query, last))

	var stubs []any
	pages := 0
	for {
		page, next, ferr := listMessages(ctx, job, token, q, timeoutMS)
		if ferr != nil {
			return *ferr, nil
		}
		stubs = append(stubs, page...)
		pages++
		if next == "" {
			break
		}
		if pages >= maxBacklogPages {
			// More backlog than one scan covers. Refuse rather than drain a slice: the scan
			// runs newest-first, so the oldest end — where a drain must start — is exactly
			// what is missing, and emitting the middle would step the watermark over
			// everything below it. Nothing is lost by stopping.
			return params.ErrDetails(job, "gmail_backlog_too_deep",
				fmt.Sprintf("More than %d emails are waiting since this step last ran, "+
					"which is more than it will work through in one go. Narrow the search, "+
					"or use Runs → Retry after clearing some of the backlog. Nothing has "+
					"been skipped — this step's position is unchanged.",
					len(stubs)),
				fmt.Sprintf("scan stopped after %d pages of %d", pages, backlogPageSize)), nil
		}
		q.Set("pageToken", next)
	}

	// Oldest first: Gmail lists newest-first, so the tail is where a drain starts.
	//
	// The slice can turn out to be entirely mail already emitted, because the
	// `after:` bound is second-granular while the watermark is milliseconds. Usually
	// that costs one slot — but if as many emails share that second as the cap allows,
	// nothing is emitted, the watermark does not move, and the next poll makes the
	// identical request. That is a permanent stall on green runs, so when a slice
	// yields nothing fresh and there is more backlog above it, step the window up.
	backlog := len(stubs)
	for round := 1; ; round++ {
		slice := stubs
		if len(slice) > maxResults {
			slice = slice[len(slice)-maxResults:]
		}
		// Arrival order: right for a poll, where a flow answering each email should
		// work through a burst in the order it was sent. Matches imap_search and
		// sftp_list_files.
		slice = reversed(slice)
		msgs, dates, unresolved := hydrateAll(ctx, job, token, slice, timeoutMS)
		staleSlice := unresolved == 0 && !anyNewerThan(dates, last) && len(slice) < len(stubs)
		if staleSlice && round < maxDrainRounds {
			stubs = stubs[:len(stubs)-len(slice)]
			continue
		}
		res := emitOnlyNew(ctx, job, name, last, msgs, dates, "", unresolved, progress)
		if res.Status == core.StatusOK && backlog > len(slice) {
			params.EmitProgress(progress, job, 1, fmt.Sprintf(
				"%d emails waiting; taking the oldest %d this run, the rest follow on the next polls",
				backlog, len(slice)))
		}
		return res, nil
	}
}

// reversed copies: the caller trims `stubs` between rounds, so reversing in
// place would scramble what is left.
func reversed(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func anyNewerThan(dates []string, last string) bool {
	for _, d := range dates {
		if d != "" && newerMillis(d, last) {
			return true
		}
	}
	return false
}

func backlogQuery(query, last string) string {
	ms, err := strconv.ParseInt(last, 10, 64)
	if err != nil || ms <= 0 {
		return query
	}
	bound := "after:" + strconv.FormatInt(ms/1000, 10)
	if query == "" {
		return bound
	}
	return query + " " + bound
}

// hydrateAll expands stubs into real email records with bounded concurrency, so
// downstream steps work with emails and never ids. dates[i] carries the
// internalDate, Gmail's authoritative receive time, for the watermark.
//
// unresolved counts the entries that could not be expanded, which matters to the
// watermark and not just the log: an email nobody could fetch has no date, so it
// cannot be emitted, and if the watermark advanced past it — which it does the
// moment any NEWER email in the page fetches cleanly — it would never be offered
// again.
func hydrateAll(ctx context.Context, job core.Job, token string, stubs []any, timeoutMS int) (msgs []any, dates []string, unresolved int) {
	msgs = make([]any, len(stubs))
	dates = make([]string, len(stubs))
	var missed atomic.Int64
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i, m := range stubs {
		stub, _ := m.(map[string]any)
		id := str(stub["id"])
		msgs[i] = map[string]any{"id": id, "threadId": str(stub["threadId"])}
		if id == "" {
			// A stub with no id can never be fetched or dated, and counting it keeps the
			// watermark from stepping over whatever it was.
			missed.Add(1)
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			// A panic here would take down the whole daemon: the engine's recover wraps
			// Execute on the calling goroutine and cannot see one raised on a spawned one.
			// Count the message unresolved instead.
			defer func() {
				if r := recover(); r != nil {
					missed.Add(1)
					log.Printf("gmail_search_messages: recovered while hydrating message %s: %v", id, r)
				}
			}()
			flat, ok := hydrateMessage(ctx, job, token, id, timeoutMS)
			if !ok {
				missed.Add(1)
				return
			}
			d := str(flat["internal_date_ms"])
			if d == "" {
				missed.Add(1)
			}
			dates[i] = d
			msgs[i] = friendlyMessage(flat)
		}(i, id)
	}
	wg.Wait()
	return msgs, dates, int(missed.Load())
}

// hydrateMessage's ok is false when the message could not be resolved, which
// the caller must count — the watermark decision depends on knowing it happened.
//
// Deliberately NO retry loop, though a retry is the obvious instinct. The shared
// egress client already paces a 429/503 with a per-(tenant, host) cooldown the
// NEXT call waits out, so an in-step retry sleeps 5s per attempt holding a worker
// slot, with every concurrent sibling queued behind the same cooldown — a page
// with a handful of failures took 15s each. And that same path calls
// core.SetRetryAfter, so the retry already exists one layer up where it costs no
// worker time.
func hydrateMessage(ctx context.Context, job core.Job, token, id string, timeoutMS int) (map[string]any, bool) {
	ep := baseURL(job) + "/users/me/messages/" + url.PathEscape(id) + "?format=full"
	st, b, ferr := gmailDo(ctx, "GET", ep, token, "", nil, timeoutMS)
	if ferr != nil || st < 200 || st >= 300 {
		return nil, false
	}
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return nil, false
	}
	return flatten(raw), true
}

// emitOnlyNew filters to emails strictly newer than the stored cursor, advances
// it to the newest seen, and emits the fresh batch.
//
// First run baselines to the newest email present and emits NOTHING, so the flow
// starts watching from "now" rather than replaying the mailbox. A nothing-new run
// emits no output ports, so downstream edges go dormant — an empty poll is a
// non-event. The cursor write is at-least-once: a failed write means at worst the
// next run re-emits this batch, never a silent drop.
//
// unresolved HOLDS the watermark — see below. That is the difference between "one
// email arrives twice" and "one email is never processed", and this module has
// already picked its side of that trade.
func emitOnlyNew(
	ctx context.Context,
	job core.Job,
	cursorName, last string,
	msgs []any,
	dates []string,
	nextPageToken string,
	unresolved int,
	progress chan<- core.Progress,
) core.Result {
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
		if !first && newerMillis(d, last) {
			fresh = append(fresh, m)
		}
	}

	if unresolved > 0 && unresolved == len(msgs) && len(msgs) > 0 {
		return params.ErrDetails(job, "gmail_unresolved",
			fmt.Sprintf("None of the %d matching email(s) could be fetched from Gmail. "+
				"Nothing was skipped — this step's position is unchanged, so they are "+
				"tried again on the next run.", len(msgs)),
			"all messages.get calls failed after retries")
	}

	// Emails that could not be fetched have no date and cannot be emitted, so hold
	// the watermark: advancing past them — which happens the moment any NEWER email
	// fetches cleanly — would mean they are never offered again, silently, on a green
	// run. The cost is that the emails that DID fetch are emitted again next run,
	// which is the same trade this module documents for a failed cursor write.
	//
	// NOT on the first run, though: a baseline emits nothing by design, and holding
	// would leave the watermark unwritten — its own silent trap, where every run
	// baselines for ever while reporting success.
	switch {
	case unresolved > 0 && !first:
		params.EmitProgress(progress, job, 1, fmt.Sprintf(
			"%d of %d email(s) could not be fetched; holding this step's position so "+
				"they are retried next run (the %d that did fetch will be emitted again)",
			unresolved, len(msgs), len(fresh)))
		newCursor = last
	case unresolved > 0:
		params.EmitProgress(progress, job, 1, fmt.Sprintf(
			"%d of %d email(s) could not be fetched while establishing the starting "+
				"point; anything newer than the emails that did read will arrive next run",
			unresolved, len(msgs)))
	}

	if newCursor != "" && newCursor != last {
		// A failed write is safe once mail has been emitted — the next run re-emits.
		// The baseline run is the exception: it emitted nothing, so failing to record
		// where to start means this watcher never emits a single email while every run
		// reports success.
		if werr := cursor.Write(ctx, job.Tenant, cursorName, newCursor); werr != nil && first {
			return cursor.FailBaseline(job, werr)
		}
	}

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
