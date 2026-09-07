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
	// The Search input pin overrides the param when wired (same pattern as
	// gmail send's to/subject/body).
	queryParam, _ := params.StringOpt(job.Params, "query")
	query, ok := params.TextInputOr(job, "query", queryParam)
	if !ok {
		return params.Err(job, "bad_input", "input port 'query' must be text"), nil
	}
	pageToken, _ := params.StringOpt(job.Params, "page_token")
	timeout := params.IntDefault(job.Params, "timeout_ms", 15000)

	// Poll mode drains the backlog oldest-first and needs the watermark before
	// it can even ask Gmail the right question, so it has its own path. The
	// hand-driven page_token case stays on the single-page path below: the
	// caller is paginating themselves and must keep getting what they asked
	// for.
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

	// Opt-in watermark: only emit emails newer than the newest one seen on a
	// previous run. Off by default so an ad-hoc search still returns every
	// match; on, it turns this into a safe poll source — a published flow
	// that acts on each match won't re-process the backlog every poll or
	// blast the whole mailbox the first time it fires after publish.
	//
	// Reached here only with a hand-supplied page_token; the ordinary poll
	// goes through pollNewMail.
	if params.BoolDefault(job.Params, "only_new", false) {
		name, last, fail := readWatermark(ctx, job)
		if fail != nil {
			return *fail, nil
		}
		return emitOnlyNew(ctx, job, name, last, msgs, dates,
			parsed.NextPageToken, unresolvedN, progress), nil
	}
	if n := unresolvedN; n > 0 {
		// only_new off: there is no watermark to protect, so a stub-only entry
		// is the documented degradation. Say so rather than letting a caller
		// wonder why an email in the list has no subject.
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
	// backlogPageSize is the page size used while scanning the backlog in poll
	// mode. Gmail's ceiling is 500 and a list page carries only {id, threadId},
	// so scanning wide is cheap — it is the per-message expansion that costs,
	// and that stays capped at the user's Max emails.
	backlogPageSize = 500

	// maxBacklogPages bounds that scan. Past it the poll refuses rather than
	// guessing: see pollNewMail.
	maxBacklogPages = 4

	// maxDrainRounds bounds how many times one poll steps its window up past a
	// slice that held nothing new. Each round costs one expansion of the slice,
	// so this is a ceiling on wasted calls, not on progress — a slice that is
	// entirely stale means the emails in it were already emitted, and the
	// window only steps toward mail that has not been. Three is generous: it
	// takes maxResults×3 emails inside the watermark's own second to exhaust.
	maxDrainRounds = 3
)

// readWatermark reads this node's stored position. fail is non-nil when the
// position could not be determined, in which case the caller must stop
// without writing anything (see cursor.Read).
func readWatermark(ctx context.Context, job core.Job) (name, last string, fail *core.Result) {
	// cursor.gmail_search.<graph>.<node>: per-(flow,node) watermark = the
	// newest internalDate we've already emitted. The store hides the
	// "cursor." prefix from the Credentials UI.
	name = fmt.Sprintf("cursor.gmail_search.%s.%s", job.GraphID, job.NodeID)
	last, err := cursor.Read(ctx, job.Tenant, name)
	if err != nil {
		// Reading this as a first run would re-baseline to the newest message
		// present and mark everything that arrived since the last poll as
		// handled — mail skipped for good, on a run that reported success.
		res := cursor.FailRead(job, err)
		return name, "", &res
	}
	return name, last, nil
}

// listMessages runs one messages.list call, returning the {id, threadId}
// stubs and the token for the next page.
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

// pollNewMail is the only_new path: emit each email that arrived since the
// last run, exactly once, oldest first.
//
// It exists as its own path because a poll has to ask Gmail a different
// question from an ad-hoc search, and the old code asked the search's
// question. messages.list returns the newest maxResults matches, so with a
// 50-email cap and 200 new emails the poll saw the newest 50, emitted them,
// and advanced the watermark to the newest of them — putting the other 150
// permanently behind the watermark. Silently, on a green run. No watermark
// arithmetic can fix that, because the 150 are never in the response at all:
// the QUERY has to change.
//
// So two changes together:
//
//   - `after:<watermark>` goes into the query, so the result set is the
//     backlog itself rather than the newest mail in the mailbox. That alone
//     stops the cap being spent on already-seen email.
//
//   - the backlog is scanned (ids only, cheap) and drained from its OLDEST
//     end, capped at maxResults. The watermark then advances only as far as
//     the emails actually emitted, so the remainder is still in front of it
//     and the next poll continues in arrival order. Same shape as
//     sftp_list_files, which caps oldest-first for exactly this reason.
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

	// Scan the backlog. Only ids come back here; nothing is expanded yet.
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
			// More backlog than this step will scan in one go. Refuse rather
			// than drain a slice of it: the scan runs newest-first, so the
			// oldest end — the end a drain must start from — is exactly what
			// is missing, and emitting the middle would step the watermark
			// over everything below it. Nothing is lost by stopping: the
			// watermark is untouched, so the whole backlog is still waiting.
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

	// Oldest first: Gmail lists newest-first, so the tail is the oldest mail
	// and that is where a drain has to start. Everything above the slice stays
	// in front of the watermark for the next poll.
	//
	// The slice can turn out to be entirely mail this step has ALREADY
	// emitted, because the `after:` bound is second-granular while the
	// watermark is milliseconds (see backlogQuery): every email sharing the
	// watermark's second comes back, and the oldest of them sort below it.
	// Usually that costs one slot. But if as many emails share that second as
	// the email cap allows — a mailing-list blast lands fifty in one second —
	// the whole slice is stale, nothing is emitted, the watermark does not
	// move, and the next poll makes the identical request. That is a permanent
	// stall on green runs, which is the failure this whole file is trying to
	// stop having. So when a slice yields nothing fresh and there is more
	// backlog above it, step the window up and look again.
	backlog := len(stubs)
	for round := 1; ; round++ {
		slice := stubs
		if len(slice) > maxResults {
			slice = slice[len(slice)-maxResults:]
		}
		// Emit in arrival order. Gmail lists newest-first, which is right for
		// an ad-hoc search but backwards for a poll: a flow that answers each
		// email, or appends it to a sheet, should work through a burst in the
		// order it was sent. Matches imap_search (ascending UID) and
		// sftp_list_files (oldest first).
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

// reversed copies a slice back-to-front. The copy matters: the caller trims
// `stubs` between rounds, so reversing in place would scramble what is left.
func reversed(in []any) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

// anyNewerThan reports whether any resolved date is past the watermark — i.e.
// whether this slice holds anything the step has not already emitted.
func anyNewerThan(dates []string, last string) bool {
	for _, d := range dates {
		if d != "" && newerMillis(d, last) {
			return true
		}
	}
	return false
}

// backlogQuery ANDs the user's search with an `after:` bound derived from the
// watermark, so messages.list returns the backlog instead of the newest mail
// in the mailbox.
//
// Gmail's `after:` takes epoch SECONDS while the watermark is milliseconds, so
// the bound is floored to the second and is therefore slightly generous —
// mail from within the watermark's own second can come back. That is harmless:
// emitOnlyNew still compares millisecond-for-millisecond, so a re-included
// email is filtered there rather than re-emitted. Erring generous is the only
// safe direction; rounding up could hide an email.
//
// An unparseable watermark (nothing stored yet, or a value from some older
// format) adds no bound at all and the client-side filter carries the whole
// job, exactly as before.
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

// hydrateAll expands {id, threadId} stubs into real email records — date,
// sender, subject, body — with bounded concurrency, so downstream steps work
// with emails and never ids. dates[i] carries the message's internalDate
// (epoch ms), Gmail's authoritative receive time, for the watermark.
//
// unresolved counts the entries that could not be expanded. That count matters
// to the watermark, not just to the log: an email nobody could fetch has no
// date, so it cannot be emitted, and if the watermark then advanced past it
// (which it does as soon as any NEWER email in the same page fetches cleanly)
// it would never be offered again. One unlucky API call, one email silently
// never processed. See emitOnlyNew for what holding the watermark does about it.
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
			// A stub with no id can never be fetched or dated. Counting it
			// keeps the watermark from stepping over whatever it was.
			missed.Add(1)
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			// A panic in here would take down the whole daemon, not just this
			// job: the engine's recover wraps Execute on the calling
			// goroutine and cannot see a panic raised on one we spawned. Count
			// the message as unresolved instead — the caller already tolerates
			// a stub-only entry for a fetch that failed.
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
				// Fetched, but Gmail gave no internalDate: there is nothing to
				// compare against the watermark, so this email can never be
				// emitted by the only_new path. Same hole as a failed fetch.
				missed.Add(1)
			}
			dates[i] = d
			msgs[i] = friendlyMessage(flat)
		}(i, id)
	}
	wg.Wait()
	return msgs, dates, int(missed.Load())
}

// hydrateMessage fetches one message and returns it flattened. ok is false
// when it could not be resolved, which the caller must count — the watermark
// decision depends on knowing it happened.
//
// Deliberately NO retry loop here, though a retry is the obvious instinct.
// Two reasons, both from the layers around this call:
//
//   - The shared egress client already paces a 429/503: it sets a per-(tenant,
//     host) cooldown (fallbackCooldown, 5s, when the response carries no
//     Retry-After) that the NEXT call to that host waits out. So an in-step
//     retry does not retry quickly — it sleeps 5s per attempt, holding a
//     worker slot, and every concurrent sibling fetch queues behind the same
//     cooldown. A page with a handful of failures took 15s per failure, and a
//     full page could outlast the node's own timeout.
//
//   - That same 429/503 path calls core.SetRetryAfter, which tells the
//     engine's retry scheduler to requeue this node after the interval the
//     server asked for. The retry already exists, one layer up, where it costs
//     no worker time.
//
// What this function owes its caller is therefore an honest "could not read
// it", not a heroic attempt — the watermark hold is what makes an unread email
// come back around.
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
//
// unresolved is how many of the page's emails could not be fetched, and it
// HOLDS the watermark: see holdWatermark. That is the difference between "one
// email arrives twice" and "one email is never processed", and this module
// already picked its side of that trade — at-least-once, never a silent drop.
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
		// On the first run we emit nothing; every match only advances the
		// baseline above.
		if !first && newerMillis(d, last) {
			fresh = append(fresh, m)
		}
	}

	// Every email in the page failed to fetch. That is an outage, not a quiet
	// poll, and the two are indistinguishable downstream: both emit nothing.
	// Fail so it is visible and the watermark stays put. (Same rule for_each
	// applies when every item fails.)
	if unresolved > 0 && unresolved == len(msgs) && len(msgs) > 0 {
		return params.ErrDetails(job, "gmail_unresolved",
			fmt.Sprintf("None of the %d matching email(s) could be fetched from Gmail. "+
				"Nothing was skipped — this step's position is unchanged, so they are "+
				"tried again on the next run.", len(msgs)),
			"all messages.get calls failed after retries")
	}

	// One or more emails could not be fetched, so they have no date and cannot
	// be emitted. Hold the watermark where it is: advancing it past them —
	// which happens the moment any NEWER email in the page fetches cleanly —
	// would mean they are never offered again, silently, on a green run.
	//
	// The cost is that the emails that DID fetch are emitted again on the next
	// run. That is the trade this module already documents for a failed cursor
	// write, chosen the same way: an email arriving twice is a nuisance a
	// person can see, an email never arriving is not. The residual case is an
	// email that fails permanently AND keeps matching the search, which would
	// re-emit its page every poll; a 4xx is far more likely to be a message
	// deleted between the list and the fetch, which the next search no longer
	// returns, and a failure affecting every email fails the step above.
	//
	// NOT on the first run, though. A baseline emits nothing by design, so an
	// email that could not be fetched loses nothing by being baselined over —
	// and holding would leave the watermark unwritten, which is its own
	// silent trap: the next run baselines too, for ever, emitting nothing
	// while every run reports success (see cursor.FailBaseline). Baselining to
	// the newest email we COULD read is also the friendlier answer, since an
	// unread newest email stays newer than the baseline and arrives next run.
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
		// A failed write is at-least-once and safe once mail has been emitted:
		// the next run re-emits this batch. The baseline run is the exception —
		// it emitted nothing, so a failure to record where to start leaves the
		// next run baselining too, and a write that keeps failing means this
		// watcher never emits a single email while every run reports success.
		if werr := cursor.Write(ctx, job.Tenant, cursorName, newCursor); werr != nil && first {
			return cursor.FailBaseline(job, werr)
		}
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
