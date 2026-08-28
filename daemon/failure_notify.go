// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/internal/emailtheme"
)

// Failure-notification dispatcher. Listens on the bus for a single
// run and, when the run terminates with status=failed, POSTs a
// concise payload to the graph's configured webhook URL.
//
// Design choices:
//
//   - Per-run goroutine spawned at SubmitGraph time. The bus
//     subscribes by jobID, so a "watch all runs" listener would
//     require a different bus contract; the per-run goroutine
//     model fits today's surface.
//
//   - Best-effort delivery: one POST attempt with a 10s timeout,
//     errors logged but not retried. A retry loop would mean
//     persistent state (where to retry, how many times left); for
//     v1 the user re-runs the workflow if they want another shot.
//     The next phase upgrades this to fire through the existing
//     webhook_send drop so retry policy is shared.
//
//   - SSRF guarded: the webhook URL is tenant-supplied, so even though the
//     user configures it on their own graph, in a multi-tenant host a tenant
//     could point it at the host's internal network or cloud metadata
//     endpoint. The send goes through the shared SSRF-guarded client (blocks
//     loopback/private/link-local unless the operator opted into private
//     egress) and the operator egress allowlist is checked on the URL first —
//     the same posture as the http_request / webhook_send drops.

// failureNotifyClient, when non-nil, overrides the HTTP client the notifier
// uses (so a test can inject an httptest client). Production leaves it nil and
// resolves the shared SSRF-guarded client per send via failureNotifyHTTPClient
// — the guard's allow-private flag must be read at call time, not at package
// init (the operator opt-in is wired during daemon startup).
var failureNotifyClient *http.Client

func failureNotifyHTTPClient() *http.Client {
	if failureNotifyClient != nil {
		return failureNotifyClient
	}
	return hfnet.SafeHTTPClient(10*time.Second, hfnet.PrivateEgressAllowed())
}

// FailurePayload is the JSON shape POSTed to the configured
// webhook URL. Compact on purpose — receivers (Slack, PagerDuty,
// internal Lambdas) want structured fields they can route on, not
// a freeform message.
type FailurePayload struct {
	GraphID      string `json:"graph_id"`
	RunID        string `json:"run_id"`
	Tenant       string `json:"tenant"`
	Workspace    string `json:"workspace"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	FailedNode   string `json:"failed_node,omitempty"`
	RunURL       string `json:"run_url,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
}

// startFailureNotifier kicks off the per-run goroutine that
// watches for terminal+failed and fires the notification. Returns
// immediately; the goroutine self-terminates on terminal events,
// context cancellation, or bus closure.
//
// No-op when the run has nothing to notify — neither a per-flow
// webhook/email configured on the graph, nor a resolvable owner we
// could send an account-level failure email to. Avoids spawning a
// goroutine that would just exit on the first event; important because
// some hot deployment loops trigger many graphs.
func (s *Service) startFailureNotifier(graph core.Graph, runID string, manual bool) {
	hasWebhook := graph.FailureNotify != nil && graph.FailureNotify.Webhook != ""
	hasPerFlowEmail := graph.FailureNotify != nil && graph.FailureNotify.Email != ""
	// Account-level owner email is only worth watching for when we can
	// both resolve the owner's account (Users) and actually deliver mail
	// (Mailer). The owner's opt-out is checked lazily at failure time —
	// here we only avoid spawning when it could never fire.
	hasOwnerEmail := graph.Owner != "" && s.Users != nil && s.Mailer != nil
	if manual {
		// Somebody is watching this run fail on their screen. Both email
		// channels are off (see JobRecord.Manual); the webhook is not.
		hasPerFlowEmail = false
		hasOwnerEmail = false
	}
	if !hasWebhook && !hasPerFlowEmail && !hasOwnerEmail {
		return
	}
	// Subscribe SYNCHRONOUSLY before spawning the goroutine so a
	// dispatcher that publishes the terminal event the next
	// nanosecond after this returns can't race past us. The
	// race-recheck inside watchForFailure also handles the
	// "already done" case (worker finished between Enqueue and
	// here), so both ends are covered.
	events, cancelSub := s.bus().Subscribe(runID)
	// 1-hour bound. The caller's request ctx would be cancelled the
	// moment SubmitGraph returns, killing the watcher before the
	// graph even runs.
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	go func() {
		defer cancel()
		defer cancelSub()
		s.watchForFailure(ctx, graph, runID, events, manual)
	}()
}

func (s *Service) watchForFailure(
	ctx context.Context,
	graph core.Graph,
	runID string,
	events <-chan BusEvent,
	manual bool,
) {

	// Defensive recheck: the worker might have completed between
	// SubmitGraph and our subscribe — same race WaitGraph defends
	// against. Pull the record and short-circuit if it's already
	// terminal.
	if rec, err := s.Jobs.Get(ctx, runID); err == nil && isTerminal(rec.Status) {
		if rec.Status == core.JobStatusFailed {
			s.fireFailureNotification(ctx, graph, recToPayload(graph, rec, s.PublicBaseURL), manual)
		}
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				// Bus closed without a terminal event we observed —
				// fall back to a final record read in case the worker
				// completed during the gap.
				if rec, err := s.Jobs.Get(context.Background(), runID); err == nil &&
					rec.Status == core.JobStatusFailed {
					s.fireFailureNotification(context.Background(), graph,
						recToPayload(graph, rec, s.PublicBaseURL), manual)
				}
				return
			}
			if ev.Terminal == nil {
				continue
			}
			if ev.Terminal.Status == core.JobStatusFailed {
				// Look up the node-level failure to populate FailedNode.
				payload := terminalToPayload(graph, runID, ev.Terminal, s.PublicBaseURL)
				if payload.FailedNode == "" {
					// Bus event doesn't carry the failed node ID; query
					// the store. ListNodeRecords with Status=failed +
					// the run filter is exactly the shape we just
					// added for run-detail.
					if nodes, err := s.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
						Tenant:     graph.Tenant,
						Workspace:  graph.Workspace,
						GraphRunID: runID,
						Status:     core.JobStatusFailed,
						Limit:      1,
					}); err == nil && len(nodes) > 0 {
						payload.FailedNode = nodes[0].NodeID
					}
				}
				s.fireFailureNotification(ctx, graph, payload, manual)
			}
			return
		}
	}
}

// FailureEmailWindow is how long one failure email speaks for.
//
// A flow that breaks usually breaks repeatedly: a poll trigger every five
// minutes against a service that is down produces twelve identical failures an
// hour, and twelve identical emails teach the reader to filter the lot. So the
// first failure mails and the rest of the window is silent.
//
// An hour rather than a few minutes because the mail is not the incident
// channel — the runs list is, and the webhook is for anyone who wants a stream.
// The mail's job is "you did not know this was broken", which is answered once.
//
// Overridable with DAZYFLOW_FAILURE_EMAIL_WINDOW; zero or negative turns the
// throttle off and mails every failure, which is the pre-throttle behaviour.
var FailureEmailWindow = time.Hour

// failureEmailThrottled reports whether an email about this failure would be a
// repeat, and how many other failures of this flow it is standing in for.
//
// Derived from the run history rather than from a record of what was sent,
// which is the whole reason there is no new table here: "has this flow failed
// recently?" is already a question the job store can answer, and an answer
// derived from the runs themselves cannot drift out of step with them.
//
// The rule is one clause — no OTHER failed run of this flow inside the window —
// and it covers both shapes of flood on purpose. A flow that stays broken has a
// prior failure every time, so exactly one email goes out. A flow that FLAPS
// (fail, succeed, fail, succeed) would defeat a "first failure of a streak"
// rule, and does not defeat this one.
//
// Fails OPEN: if the store cannot answer, the mail goes. A throttle that eats
// an alert when the database hiccups is worse than one that sends a duplicate.
func (s *Service) failureEmailThrottled(ctx context.Context, graph core.Graph, runID string) (bool, int) {
	if FailureEmailWindow <= 0 || s.Jobs == nil {
		return false, 0
	}
	// Bounded: the count is for a log line, and a flow failing more than this
	// in an hour is already comprehensively described by "a lot".
	const scan = 100
	runs, err := s.Jobs.ListGraphRuns(ctx, core.ListGraphRunsOpts{
		Tenant:    graph.Tenant,
		Workspace: graph.Workspace,
		GraphID:   graph.ID,
		Status:    core.JobStatusFailed,
		// Since bounds EnqueuedAt, which is the only time the store filters on.
		// For the flows this protects against — a trigger firing on a schedule —
		// enqueue and finish are seconds apart, so the difference does not
		// matter; a run that started three hours ago and fails now is treated as
		// outside the window, which errs towards sending.
		Since: time.Now().Add(-FailureEmailWindow),
		Limit: scan,
	})
	if err != nil {
		return false, 0
	}
	others := 0
	for _, r := range runs {
		// The failure being reported is already terminal in the store, so it is
		// in this list and must not throttle itself.
		if r.ID != runID {
			others++
		}
	}
	return others > 0, others
}

// fireFailureNotification POSTs the payload to the graph's
// webhook URL. Errors land in the daemon log; we don't surface
// them back to the user because the dispatcher runs after the run
// has already terminated — there's no in-progress request to fail.
func (s *Service) fireFailureNotification(
	ctx context.Context,
	graph core.Graph,
	payload FailurePayload,
	manual bool,
) {
	// Both email channels are off for a run someone started in the app: they
	// are watching it fail. Checked here as well as at arming time, because
	// arming still happens for the webhook and the two must not drift.
	//
	// The throttle is the same idea one step out: the first failure in the
	// window has already said what an email can say.
	throttled, others := false, 0
	if !manual {
		throttled, others = s.failureEmailThrottled(ctx, graph, payload.RunID)
	}
	if throttled && s.Logger != nil {
		// Logged rather than silent: an operator asking "why did I not get mail
		// about that?" should find the answer here.
		s.Logger.Printf("failure email for %s/%s/%s throttled: %d other failure(s) in the last %s",
			graph.Tenant, graph.Workspace, graph.ID, others, FailureEmailWindow)
	}
	if !manual && !throttled {
		// Per-flow email channel: an explicit address configured on the
		// graph (notify some external inbox / on-call address).
		perFlowEmail := ""
		if graph.FailureNotify != nil {
			perFlowEmail = graph.FailureNotify.Email
		}
		if perFlowEmail != "" {
			s.fireFailureEmail(ctx, graph, payload, perFlowEmail)
		}
		// Account-level channel: email the flow owner if their preference
		// opts in (the default). Deduped against the per-flow address so an
		// owner who also set FailureNotify.Email to themselves gets a single
		// mail, not two.
		if to := s.ownerFailureEmail(ctx, graph); to != "" && !strings.EqualFold(to, perFlowEmail) {
			s.fireFailureEmail(ctx, graph, payload, to)
		}
	}
	if graph.FailureNotify == nil || graph.FailureNotify.Webhook == "" {
		return
	}
	url := graph.FailureNotify.Webhook
	// The URL is tenant-supplied. Enforce the operator egress allowlist on it
	// before dialing (the SSRF-guarded client below independently blocks
	// loopback/private/link-local at dial time, anti-rebinding), so the webhook
	// can't be used to probe the host's internal network or metadata endpoint.
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("webhook blocked: %w", err))
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("marshal: %w", err))
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("build request: %w", err))
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "dazyflow-failure-notify/1.0")
	resp, err := failureNotifyHTTPClient().Do(req)
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("post: %w", err))
		return
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logFailureNotifyError(graph, fmt.Errorf("non-2xx status %d", resp.StatusCode))
	}
}

// ownerFailureEmail resolves the address to send the account-level
// failure email to, or "" when there's nothing to send: no owner, no
// user store / mailer, the owner has no password account (SSO / API-key
// subjects won't resolve), or the owner has turned failure email off.
//
// The user-store lookup happens here — at failure time — rather than at
// submit, so the common success path never touches the store.
func (s *Service) ownerFailureEmail(ctx context.Context, graph core.Graph) string {
	if graph.Owner == "" || s.Users == nil || s.Mailer == nil {
		return ""
	}
	// Owner is a principal subject; for password users that's their
	// email, which is what the user store is keyed by. Other owner kinds
	// simply won't resolve (treated as "no account-level email").
	u, err := s.Users.GetByEmail(ctx, graph.Owner)
	if err != nil {
		return ""
	}
	if !u.Notify.EmailOnFlowFailureEnabled() {
		return ""
	}
	return u.Email
}

// fireFailureEmail sends the plain-text failure summary to `to` through
// the operator's transactional mailer. Same best-effort contract as the
// webhook channel.
func (s *Service) fireFailureEmail(ctx context.Context, graph core.Graph, payload FailurePayload, to string) {
	if s.Mailer == nil {
		s.logFailureNotifyError(graph, fmt.Errorf("email channel configured but no mailer on this deployment (set DAZYFLOW_SMTP_URL)"))
		return
	}
	name := graph.Name
	if name == "" {
		name = graph.ID
	}
	// Addressed to an account holder — the flow's owner — so it goes out in
	// THEIR language, not the flow's: this is the platform telling a person
	// their thing broke, not the flow speaking to its readers.
	m := s.mailMsgs(ctx, to)
	var facts []emailtheme.Fact
	if payload.FailedNode != "" {
		facts = append(facts, emailtheme.Fact{Label: m.FactStep, Value: payload.FailedNode})
	}
	if payload.ErrorMessage != "" {
		errVal := payload.ErrorMessage
		if payload.ErrorCode != "" {
			errVal += " (" + payload.ErrorCode + ")"
		}
		facts = append(facts, emailtheme.Fact{Label: m.FactError, Value: errVal})
	}
	if payload.FinishedAt != "" {
		facts = append(facts, emailtheme.Fact{Label: m.FactFinishedAt, Value: payload.FinishedAt})
	}
	content := emailtheme.Content{
		Subject:   fmt.Sprintf(m.FailureSubject, name),
		Preheader: m.FailurePreheader,
		Eyebrow:   m.FailureEyebrow,
		Heading:   m.FailureHeading,
		Tone:      "danger",
		Intro:     []string{fmt.Sprintf(m.FailureIntro, name)},
		Facts:     facts,
		Outro:     []string{m.FailureOutro},
		LogoURL:   emailLogoURL(s.PublicBaseURL),
	}
	if payload.RunURL != "" {
		content.Button = &emailtheme.Button{Label: m.FailureButton, URL: payload.RunURL}
	}
	if err := s.Mailer.SendThemed(ctx, to, emailtheme.PlainText(content), content); err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("email: %w", err))
	}
}

func (s *Service) logFailureNotifyError(graph core.Graph, err error) {
	// Service doesn't carry its own logger today; log to the
	// standard log package which the gateway's middleware shares.
	// Format matches the rest of the daemon's structured-ish logs.
	if s.Logger != nil {
		s.Logger.Printf("failure-notify [%s/%s/%s]: %v",
			graph.Tenant, graph.Workspace, graph.ID, err)
	}
}

// recToPayload populates a FailurePayload from a record that's
// already terminal (the "race-recheck" path).
func recToPayload(graph core.Graph, rec core.JobRecord, baseURL string) FailurePayload {
	p := FailurePayload{
		GraphID:   graph.ID,
		RunID:     rec.ID,
		Tenant:    graph.Tenant,
		Workspace: graph.Workspace,
	}
	if rec.Result != nil && rec.Result.Error != nil {
		p.ErrorCode = rec.Result.Error.Code
		p.ErrorMessage = rec.Result.Error.Message
	}
	if rec.FinishedAt != nil {
		p.FinishedAt = rec.FinishedAt.UTC().Format(time.RFC3339)
	}
	p.RunURL = buildRunURL(baseURL, graph.Tenant, rec.ID)
	return p
}

// terminalToPayload populates a FailurePayload from a TerminalEvent
// observed off the bus (the common path — the watcher's actually
// watching for these).
func terminalToPayload(graph core.Graph, runID string, t *TerminalEvent, baseURL string) FailurePayload {
	p := FailurePayload{
		GraphID:    graph.ID,
		RunID:      runID,
		Tenant:     graph.Tenant,
		Workspace:  graph.Workspace,
		FinishedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if t.Error != nil {
		p.ErrorCode = t.Error.Code
		p.ErrorMessage = t.Error.Message
	}
	p.RunURL = buildRunURL(baseURL, graph.Tenant, runID)
	return p
}

// buildRunURL constructs a UI-facing link to the run-detail page
// when the daemon knows its own public origin. Empty string when
// PublicBaseURL isn't set — receivers should fall back to the
// graph_id/run_id fields in that case.
//
// The link is pinned to the run's org (see withOrg in orglink.go): a run is
// only visible inside its own org, so a recipient who last used a different one
// would otherwise be told the run doesn't exist.
func buildRunURL(baseURL, tenant, runID string) string {
	if baseURL == "" {
		return ""
	}
	return withOrg(strings.TrimRight(baseURL, "/")+"/runs/"+runID, tenant)
}
