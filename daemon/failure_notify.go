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

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/internal/emailtheme"
)

// Failure notification: when a run ends badly, tell the flow's owner by email
// and POST a concise payload to any webhook the flow configures.
//
// Design choices:
//
//   - Driven by a SWEEP over the store (SweepFailureNotifications), not by a
//     watcher armed when the run starts. The watcher version lost exactly the
//     notifications worth having — see that function for the list. The store
//     holds the "has this been told?" bit, so no process can forget it.
//
//   - Retried, bounded: a send that fails releases its claim and a later pass
//     tries again, up to notifyMaxAttempts. Only transient causes retry; a
//     blocked URL or a 4xx is the receiver saying no.
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

// NotifySweepLookback bounds how far back a sweep will look for runs still
// owing a notification. It is the answer to "the daemon was down for three
// days — do I now get 900 emails?": no, only the ones from the last hour.
// Anything older is in the Runs list, which is where a three-day-old failure
// belongs.
var NotifySweepLookback = time.Hour

// notifyMaxAttempts bounds retries of one run's notification, so a mail host
// that refuses forever cannot make a sweep re-send forever.
const notifyMaxAttempts = 3

// notifySweepBatch caps one pass, so a burst of failures is worked through
// over several passes instead of holding a sweep open through hundreds of
// SMTP conversations.
const notifySweepBatch = 25

// SweepFailureNotifications notifies the runs that have failed and not been
// told about, and is the whole of failure notification — there is no per-run
// watcher any more.
//
// It replaced one: a goroutine spawned at submit time, subscribed to the bus,
// bounded to an hour. Everything about that shape lost notifications exactly
// when they mattered most, because the goroutine lived in the process that
// happened to accept the submission:
//
//   - a restart or deploy killed every watcher, so any run in flight at that
//     moment failed silently, for ever;
//   - a run recovered from an expired lease by ANOTHER replica had no watcher
//     on the replica that finished it;
//   - the orphaned-run reaper closes runs a crash stranded, and nothing was
//     listening for those either;
//   - and the one-hour ceiling meant every approval, every long delay and
//     every retry backoff outlived its own watcher.
//
// A sweep over what the store already knows has none of those failure modes,
// and it is also what makes a cancelled-by-timeout run notifiable at all (the
// old watcher only ever looked for `failed`).
//
// Claim-then-send: ClaimUnnotified stamps each run before handing it over, so
// two replicas sweeping the same instant cannot both mail about one run. A
// send that fails releases its claim so a later pass retries it, bounded by
// notifyMaxAttempts.
func (s *Service) SweepFailureNotifications(ctx context.Context) {
	notifier, ok := s.Jobs.(core.FailureNotifier)
	if !ok {
		return
	}
	runs, err := notifier.ClaimUnnotified(ctx, NotifySweepLookback, notifyMaxAttempts, notifySweepBatch)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Printf("failure-notify sweep: claim: %v", err)
		}
		return
	}
	for _, rec := range runs {
		if !s.notifyOneRun(ctx, rec) {
			if rerr := notifier.ReleaseNotifyClaim(ctx, rec.ID); rerr != nil && s.Logger != nil {
				s.Logger.Printf("failure-notify sweep: release %s: %v", rec.ID, rerr)
			}
		}
	}
}

// notifyOneRun sends the notifications one claimed run owes. Reports whether
// the claim should stand: false asks for a retry on a later pass.
func (s *Service) notifyOneRun(ctx context.Context, rec core.JobRecord) bool {
	if !notifiableOutcome(rec) {
		// Nothing to say, and nothing to retry — the claim stands so the run
		// is not reconsidered on every pass for the rest of the lookback.
		return true
	}
	if len(rec.GraphPayload) == 0 {
		// No flow to read a notification target from. Not retryable.
		return true
	}
	var g core.Graph
	if err := json.Unmarshal(rec.GraphPayload, &g); err != nil {
		if s.Logger != nil {
			s.Logger.Printf("failure-notify sweep: run %s has unparseable payload: %v", rec.ID, err)
		}
		return true
	}
	payload := recToPayload(g, rec, s.PublicBaseURL)
	if payload.FailedNode == "" {
		// Name the step that broke, which is the first thing the reader wants.
		if nodes, err := s.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
			Tenant:     g.Tenant,
			Workspace:  g.Workspace,
			GraphRunID: rec.ID,
			Status:     core.JobStatusFailed,
			Limit:      1,
		}); err == nil && len(nodes) > 0 {
			payload.FailedNode = nodes[0].NodeID
		}
	}
	return s.fireFailureNotification(ctx, g, payload) == nil
}

// notifiableOutcome reports whether this run's ending is one to tell somebody
// about.
//
// Failed always is. Cancelled depends on WHO cancelled: a person stopping
// their own run does not need an email about it, but the platform stopping a
// run — the wall-clock timeout — is precisely the case the old watcher missed
// entirely, and the one that reads most like a betrayal when it is silent.
// The run just says "cancelled" in the list, which looks like somebody
// meant it.
func notifiableOutcome(rec core.JobRecord) bool {
	switch rec.Status {
	case core.JobStatusFailed:
		return true
	case core.JobStatusCancelled:
		return rec.Result != nil && rec.Result.Error != nil &&
			rec.Result.Error.Code != CancelCodeByPerson
	default:
		return false
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
// repeat, and how many failures of this flow the PREVIOUS window held.
//
// Derived from the run history rather than from a record of what was sent,
// which is the whole reason there is no new table here: "has this flow failed
// recently?" is already a question the job store can answer, and an answer
// derived from the runs themselves cannot drift out of step with them.
//
// The window is a TUMBLING one — now truncated to FailureEmailWindow — not a
// sliding one, and that is the fix for a real gap. The sliding version asked
// "any other failure in the last hour?", which a flow that keeps failing
// always answers yes to: it sent one email at the start of an outage and then
// nothing, ever, however long the outage lasted. A flow broken for a week
// produced a single email, sent a week ago, and the documented fallback —
// somebody noticing in the Runs list — is exactly the assumption that does not
// hold. With tumbling windows a continuing outage gets one email per window,
// which is the difference between "you did not know this was broken" and
// "you still do not know this is broken".
//
// It still covers both shapes of flood. A flow that stays broken mails once
// per window rather than once per run; a flow that FLAPS (fail, succeed, fail,
// succeed) would defeat a "first failure of a streak" rule and does not defeat
// this one. The cost of tumbling is a boundary case: failures at 10:59 and
// 11:01 both mail. Two emails two minutes apart, once, is a much smaller
// problem than silence for a week.
//
// The second return is the previous window's count, so the mail can say how
// bad the gap it is standing in for was. Counting the CURRENT window would be
// useless: the email goes out on that window's first failure, when the count
// is still zero.
//
// Fails OPEN: if the store cannot answer, the mail goes. A throttle that eats
// an alert when the database hiccups is worse than one that sends a duplicate.
func (s *Service) failureEmailThrottled(ctx context.Context, graph core.Graph, runID string) (bool, int) {
	if FailureEmailWindow <= 0 || s.Jobs == nil {
		return false, 0
	}
	// Bounded: the count is a rounding number in a sentence, and a flow
	// failing more than this in a window is already described by "a lot".
	const scan = 200
	window := time.Now().Truncate(FailureEmailWindow)
	runs, err := core.ListRunSummaries(ctx, s.Jobs, core.ListGraphRunsOpts{
		Tenant:    graph.Tenant,
		Workspace: graph.Workspace,
		GraphID:   graph.ID,
		Status:    core.JobStatusFailed,
		// Since bounds EnqueuedAt, which is the only time the store filters on.
		// For the flows this protects against — a trigger firing on a schedule —
		// enqueue and finish are seconds apart, so the difference does not
		// matter; a run that started three hours ago and fails now is treated as
		// outside the window, which errs towards sending.
		Since: window.Add(-FailureEmailWindow),
		Limit: scan,
	})
	if err != nil {
		return false, 0
	}
	thisWindow, previousWindow := 0, 0
	for _, r := range runs {
		// The failure being reported is already terminal in the store, so it is
		// in this list and must not throttle itself.
		if r.ID == runID {
			continue
		}
		if r.EnqueuedAt.Before(window) {
			previousWindow++
		} else {
			thisWindow++
		}
	}
	return thisWindow > 0, previousWindow
}

// fireFailureNotification POSTs the payload to the graph's
// webhook URL. Errors land in the daemon log; we don't surface
// them back to the user because the dispatcher runs after the run
// has already terminated — there's no in-progress request to fail.
func (s *Service) fireFailureNotification(
	ctx context.Context,
	graph core.Graph,
	payload FailurePayload,
) error {
	// A run someone started from the app used to have both email channels
	// switched off, on the reasoning that they were watching it fail on their
	// screen. That reasoning did not survive contact with the ways a run gets
	// started: the same endpoints serve dzctl, the MCP server and anyone's own
	// cron, so "manual" meant "nobody is told" for every API-driven run — the
	// unattended case that needs telling most. Nothing distinguishes those
	// callers server-side (an API-key principal and a session principal look
	// alike), so the suppression is gone and the throttle below carries the
	// job instead: someone iterating on a broken flow gets one email an hour,
	// not one per attempt. JobRecord.Manual still gates breakpoints, which is
	// what it was really for.
	throttled, priorFailures := s.failureEmailThrottled(ctx, graph, payload.RunID)
	if throttled && s.Logger != nil {
		// Logged rather than silent: an operator asking "why did I not get mail
		// about that?" should find the answer here.
		s.Logger.Printf("failure email for %s/%s/%s throttled: already mailed this %s window",
			graph.Tenant, graph.Workspace, graph.ID, FailureEmailWindow)
	}
	if !throttled {
		// Per-flow email channel: an explicit address configured on the
		// graph (notify some external inbox / on-call address).
		perFlowEmail := ""
		if graph.FailureNotify != nil {
			perFlowEmail = graph.FailureNotify.Email
		}
		if perFlowEmail != "" {
			if err := s.fireFailureEmail(ctx, graph, payload, perFlowEmail, priorFailures); err != nil {
				return err
			}
		}
		// Account-level channel: email the flow owner if their preference
		// opts in (the default). Deduped against the per-flow address so an
		// owner who also set FailureNotify.Email to themselves gets a single
		// mail, not two.
		if to := s.ownerFailureEmail(ctx, graph); to != "" && !strings.EqualFold(to, perFlowEmail) {
			if err := s.fireFailureEmail(ctx, graph, payload, to, priorFailures); err != nil {
				return err
			}
		}
	}
	if graph.FailureNotify == nil || graph.FailureNotify.Webhook == "" {
		return nil
	}
	url := graph.FailureNotify.Webhook
	// The URL is tenant-supplied. Enforce the operator egress allowlist on it
	// before dialing (the SSRF-guarded client below independently blocks
	// loopback/private/link-local at dial time, anti-rebinding), so the webhook
	// can't be used to probe the host's internal network or metadata endpoint.
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		// A blocked URL will be blocked next pass too — not worth retrying.
		s.logFailureNotifyError(graph, fmt.Errorf("webhook blocked: %w", err))
		return nil
	}
	// The failure webhook is a tenant-supplied URL and this instance's own
	// form and trigger endpoints are URLs like any other, so pointing a
	// failing flow's webhook at its own form made every failure submit the
	// next one — a loop with no step in it, at ~120 runs a second, and the
	// throttle above covers the email channels only. Carry the failed run's
	// place in the chain so the shared client stamps depth+1 on a call that
	// comes back to us and the endpoint refuses past the cap.
	ctx = core.WithTriggerDepth(ctx, s.runTriggerDepth(ctx, payload.RunID))
	body, err := json.Marshal(payload)
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("marshal: %w", err))
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("build request: %w", err))
		return nil
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "dazyflow-failure-notify/1.0")
	// A transport error or a 5xx is worth another pass; a 4xx is the receiver
	// saying no, and repeating it changes nothing.
	resp, err := failureNotifyHTTPClient().Do(req)
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("post: %w", err))
		return err
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode >= 500 {
		err := fmt.Errorf("non-2xx status %d", resp.StatusCode)
		s.logFailureNotifyError(graph, err)
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logFailureNotifyError(graph, fmt.Errorf("non-2xx status %d", resp.StatusCode))
	}
	return nil
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
// priorFailures is how many runs of this flow failed in the window before
// this one, which turns a repeat email from a duplicate into an escalation:
// the reader learns the flow has been failing all along, not just now.
func (s *Service) fireFailureEmail(ctx context.Context, graph core.Graph, payload FailurePayload, to string, priorFailures int) error {
	if s.Mailer == nil {
		// A missing mailer is a deployment fact, not a transient one.
		s.logFailureNotifyError(graph, fmt.Errorf("email channel configured but no mailer on this deployment (set DAZYFLOW_SMTP_URL)"))
		return nil
	}
	name := graph.Name
	if name == "" {
		name = graph.ID
	}
	// The name is the mail SUBJECT — bounded far tighter than a body, because a
	// header line past RFC 5321's 1000 octets makes the server drop the
	// connection and the notification is never delivered at all.
	name = core.ClipNotificationLabel(name)
	// Addressed to an account holder — the flow's owner — so it goes out in
	// THEIR language, not the flow's: this is the platform telling a person
	// their thing broke, not the flow speaking to its readers.
	m := s.mailMsgs(ctx, to)
	var facts []emailtheme.Fact
	if payload.FailedNode != "" {
		facts = append(facts, emailtheme.Fact{Label: m.FactStep, Value: core.ClipNotificationLabel(payload.FailedNode)})
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
	intro := []string{fmt.Sprintf(m.FailureIntro, name)}
	if priorFailures > 0 {
		// Not the first time. Say so, with the number, because "it failed" and
		// "it has failed 47 times and you have not noticed" call for different
		// reactions and used to read identically.
		intro = append(intro, fmt.Sprintf(m.FailureStillBroken, priorFailures))
	}
	content := emailtheme.Content{
		Subject:   fmt.Sprintf(m.FailureSubject, name),
		Preheader: m.FailurePreheader,
		Eyebrow:   m.FailureEyebrow,
		Heading:   m.FailureHeading,
		Tone:      "danger",
		Intro:     intro,
		Facts:     facts,
		Outro:     []string{m.FailureOutro},
		LogoURL:   emailLogoURL(s.PublicBaseURL),
	}
	if payload.RunURL != "" {
		content.Button = &emailtheme.Button{Label: m.FailureButton, URL: payload.RunURL}
	}
	// Returned, not just logged: the sweep releases its claim on a failed send
	// so a later pass tries again. A mail host having a bad minute used to mean
	// the notification was simply gone.
	if err := s.Mailer.SendThemed(ctx, to, emailtheme.PlainText(content), content); err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("email: %w", err))
		return err
	}
	return nil
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
		// Bounded where the payload is built, so the ceiling covers the mail
		// and the third-party webhook alike — a step's error message is as
		// free-form as any other run output.
		p.ErrorMessage = core.ClipNotificationText(rec.Result.Error.Message)
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
		p.ErrorMessage = core.ClipNotificationText(t.Error.Message)
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
