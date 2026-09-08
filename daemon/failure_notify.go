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
			rec.Result.Error.Code != CancelCodeByPerson
	default:
		return false
	}
}

var FailureEmailWindow = time.Hour

// Throttled per (flow, error) so a flow failing every minute sends one mail, not
// sixty. A MANUAL run is never mailed about: someone is watching it.
func (s *Service) failureEmailThrottled(ctx context.Context, graph core.Graph, runID string) (bool, int) {
	if FailureEmailWindow <= 0 || s.Jobs == nil {
		return false, 0
	}
	const scan = 200
	window := time.Now().Truncate(FailureEmailWindow)
	runs, err := core.ListRunSummaries(ctx, s.Jobs, core.ListGraphRunsOpts{
		Tenant:    graph.Tenant,
		Workspace: graph.Workspace,
		GraphID:   graph.ID,
		Status:    core.JobStatusFailed,
		// EnqueuedAt is the only time the store filters on.
		Since: window.Add(-FailureEmailWindow),
		Limit: scan,
	})
	if err != nil {
		return false, 0
	}
	thisWindow, previousWindow := 0, 0
	for _, r := range runs {
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

func (s *Service) fireFailureNotification(
	ctx context.Context,
	graph core.Graph,
	payload FailurePayload,
) error {
	throttled, priorFailures := s.failureEmailThrottled(ctx, graph, payload.RunID)
	if throttled && s.Logger != nil {
		s.Logger.Printf("failure email for %s/%s/%s throttled: already mailed this %s window",
			graph.Tenant, graph.Workspace, graph.ID, FailureEmailWindow)
	}
	if !throttled {
		perFlowEmail := ""
		if graph.FailureNotify != nil {
			perFlowEmail = graph.FailureNotify.Email
		}
		if perFlowEmail != "" {
			if err := s.fireFailureEmail(ctx, graph, payload, perFlowEmail, priorFailures); err != nil {
				return err
			}
		}
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
