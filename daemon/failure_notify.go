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
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/internal/emailtheme"
)

// Told once per failed run, from a DURABLE claim rather than a per-run goroutine:
// the old form was lost on every restart, so the failures most worth hearing about
// were the least likely to be reported.

// Tests override it; production leaves it nil so the guarded doer applies.
var failureNotifyClient *http.Client

func failureNotifyHTTPClient() *http.Client {
	if failureNotifyClient != nil {
		return failureNotifyClient
	}
	return hfnet.SafeHTTPClient(10*time.Second, hfnet.PrivateEgressAllowed())
}

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

var NotifySweepLookback = time.Hour

const notifyMaxAttempts = 3

const notifySweepBatch = 25

// Claims atomically before sending, so two replicas sweeping the same instant
// cannot both mail about one run. A send that fails releases its claim, and the
// attempt still counts, so a persistently failing notification is bounded rather
// than retried for ever.
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

func (s *Service) notifyOneRun(ctx context.Context, rec core.JobRecord) bool {
	if !notifiableOutcome(rec) {
		return true
	}
	if len(rec.GraphPayload) == 0 {
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

func notifiableOutcome(rec core.JobRecord) bool {
	switch rec.Status {
	case core.JobStatusFailed:
		return true
	case core.JobStatusCancelled:
		return rec.Result != nil && rec.Result.Error != nil &&
			rec.Result.Error.Code != CancelCodeByPerson &&
			rec.Result.Error.Code != CancelCodeParent
	default:
		return false
	}
}

var FailureEmailWindow = time.Hour

// Throttled per flow so a flow failing every minute sends one mail per window,
// not sixty. Manual runs are mailed about like any other (see
// failure_notify_manual_test.go): the throttle is what keeps someone iterating
// on a broken flow from getting one mail per attempt.
//
// The throttle counts mails SENT, from failureMailLedger, not other failed runs:
// counting failures made two near-simultaneous failures each see the other and
// both stay silent. The ledger is per process — the sweep's claim still keeps
// each run to one notification fleet-wide, but replicas throttle independently,
// so a fleet can send one mail per window per replica.
func (s *Service) priorWindowFailures(ctx context.Context, graph core.Graph, runID string, window time.Time) int {
	if s.Jobs == nil {
		return 0
	}
	const scan = 200
	runs, err := core.ListRunSummaries(ctx, s.Jobs, core.ListGraphRunsOpts{
		Tenant:    graph.Tenant,
		Workspace: graph.Workspace,
		GraphID:   graph.ID,
		Status:    core.JobStatusFailed,
		// EnqueuedAt is the only time the store filters on.
		Since: window.Add(-FailureEmailWindow),
		Until: window,
		Limit: scan,
	})
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range runs {
		if r.ID != runID {
			n++
		}
	}
	return n
}

// failureMailLedger remembers which run holds each flow's mail for the current
// window, and which channels each run has already been delivered on, so a retry
// after a partial failure redoes only what failed rather than mailing twice.
type failureMailLedger struct {
	mu     sync.Mutex
	holder map[string]failureMailHolder // flow key → the run mailed this window
	sent   map[string]failureMailSent   // run ID → channels delivered
}

type failureMailHolder struct {
	window time.Time
	runID  string
}

type failureMailSent struct {
	at       time.Time
	channels map[string]bool
}

// admit reports whether runID may mail for the flow in this window, and takes
// the window for it when it is free.
func (l *failureMailLedger) admit(flow, runID string, window time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == nil {
		l.holder = map[string]failureMailHolder{}
	}
	if h, ok := l.holder[flow]; ok && h.window.Equal(window) && h.runID != runID {
		return false
	}
	l.holder[flow] = failureMailHolder{window: window, runID: runID}
	for k, h := range l.holder {
		if h.window.Before(window) {
			delete(l.holder, k)
		}
	}
	return true
}

// release gives the window back when runID mailed nobody, so the next failure
// is not throttled behind a mail that never went out.
func (l *failureMailLedger) release(flow, runID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if h, ok := l.holder[flow]; ok && h.runID == runID {
		for ch := range l.sent[runID].channels {
			if strings.HasPrefix(ch, "email:") {
				return
			}
		}
		delete(l.holder, flow)
	}
}

func (l *failureMailLedger) delivered(runID, channel string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sent[runID].channels[channel]
}

func (l *failureMailLedger) markDelivered(runID, channel string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sent == nil {
		l.sent = map[string]failureMailSent{}
	}
	// A run is retried only inside the sweep's lookback, so older entries are dead.
	keep := NotifySweepLookback + FailureEmailWindow + time.Hour
	for id, e := range l.sent {
		if now.Sub(e.at) > keep {
			delete(l.sent, id)
		}
	}
	e, ok := l.sent[runID]
	if !ok {
		e = failureMailSent{at: now, channels: map[string]bool{}}
		l.sent[runID] = e
	}
	e.channels[channel] = true
}

func (s *Service) fireFailureNotification(
	ctx context.Context,
	graph core.Graph,
	payload FailurePayload,
) error {
	ledger := &s.failureMail
	flow := graph.Tenant + "/" + graph.Workspace + "/" + graph.ID
	admitted, priorFailures := true, 0
	if FailureEmailWindow > 0 {
		window := time.Now().Truncate(FailureEmailWindow)
		admitted = ledger.admit(flow, payload.RunID, window)
		if admitted {
			priorFailures = s.priorWindowFailures(ctx, graph, payload.RunID, window)
		}
	}
	if !admitted && s.Logger != nil {
		s.Logger.Printf("failure email for %s/%s/%s throttled: already mailed this %s window",
			graph.Tenant, graph.Workspace, graph.ID, FailureEmailWindow)
	}
	if admitted {
		perFlowEmail := ""
		if graph.FailureNotify != nil {
			perFlowEmail = graph.FailureNotify.Email
		}
		mail := func(to string) error {
			ch := "email:" + strings.ToLower(to)
			if ledger.delivered(payload.RunID, ch) {
				return nil
			}
			if err := s.fireFailureEmail(ctx, graph, payload, to, priorFailures); err != nil {
				ledger.release(flow, payload.RunID)
				return err
			}
			ledger.markDelivered(payload.RunID, ch)
			return nil
		}
		if perFlowEmail != "" {
			if err := mail(perFlowEmail); err != nil {
				return err
			}
		}
		if to := s.ownerFailureEmail(ctx, graph); to != "" && !strings.EqualFold(to, perFlowEmail) {
			if err := mail(to); err != nil {
				return err
			}
		}
	}
	if graph.FailureNotify == nil || graph.FailureNotify.Webhook == "" {
		return nil
	}
	webhookChannel := "webhook:" + graph.FailureNotify.Webhook
	if ledger.delivered(payload.RunID, webhookChannel) {
		return nil
	}
	url := graph.FailureNotify.Webhook
	// Tenant-supplied, so it gets the operator egress allowlist and the SSRF guard.
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("webhook blocked: %w", err))
		return nil
	}
	// A tenant-supplied URL, so it must not be able to point back at this instance's
	// own trigger endpoints and drive a loop.
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
	resp, err := failureNotifyHTTPClient().Do(req)
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("post: %w", err))
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode >= 500 {
		err := fmt.Errorf("non-2xx status %d", resp.StatusCode)
		s.logFailureNotifyError(graph, err)
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logFailureNotifyError(graph, fmt.Errorf("non-2xx status %d", resp.StatusCode))
	}
	ledger.markDelivered(payload.RunID, webhookChannel)
	return nil
}

// Resolves the account-level address, which is not the same as the flow's webhook.
func (s *Service) ownerFailureEmail(ctx context.Context, graph core.Graph) string {
	if graph.Owner == "" || s.Users == nil || s.Mailer == nil {
		return ""
	}
	u, err := s.Users.GetByEmail(ctx, graph.Owner)
	if err != nil {
		return ""
	}
	if !u.Notify.EmailOnFlowFailureEnabled() {
		return ""
	}
	return u.Email
}

func (s *Service) fireFailureEmail(ctx context.Context, graph core.Graph, payload FailurePayload, to string, priorFailures int) error {
	if s.Mailer == nil {
		s.logFailureNotifyError(graph, fmt.Errorf("email channel configured but no mailer on this deployment (set DAZYFLOW_SMTP_URL)"))
		return nil
	}
	name := graph.Name
	if name == "" {
		name = graph.ID
	}
	// The SUBJECT: RFC 5321 caps a line at 1000 octets, and a longer one is dropped.
	name = core.ClipNotificationLabel(name)
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
	if err := s.Mailer.SendThemed(ctx, to, emailtheme.PlainText(content), content); err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("email: %w", err))
		return err
	}
	return nil
}

func (s *Service) logFailureNotifyError(graph core.Graph, err error) {
	if s.Logger != nil {
		s.Logger.Printf("failure-notify [%s/%s/%s]: %v",
			graph.Tenant, graph.Workspace, graph.ID, err)
	}
}

func recToPayload(graph core.Graph, rec core.JobRecord, baseURL string) FailurePayload {
	p := FailurePayload{
		GraphID:   graph.ID,
		RunID:     rec.ID,
		Tenant:    graph.Tenant,
		Workspace: graph.Workspace,
	}
	if rec.Result != nil && rec.Result.Error != nil {
		p.ErrorCode = rec.Result.Error.Code
		// Bounded where the payload is built, so it covers the mail and the webhook alike.
		p.ErrorMessage = core.ClipNotificationText(rec.Result.Error.Message)
	}
	if rec.FinishedAt != nil {
		p.FinishedAt = rec.FinishedAt.UTC().Format(time.RFC3339)
	}
	p.RunURL = buildRunURL(baseURL, graph.Tenant, rec.ID)
	return p
}

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

func buildRunURL(baseURL, tenant, runID string) string {
	if baseURL == "" {
		return ""
	}
	return withOrg(strings.TrimRight(baseURL, "/")+"/runs/"+runID, tenant)
}
