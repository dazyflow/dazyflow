// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/emailtheme"
)

// A hosted form is an open door, so a record per refused submission is a
// table-growth vector.

// A refused SCHEDULED fire is recorded as a terminal `skipped` run with no
// payload — nothing was lost, the trigger comes round again. A refused INBOUND
// delivery is recorded as `failed` WITH the payload, because someone typed
// something and it is gone unless it is kept. The asymmetry is the point.
//
// A hosted form is an open door, so a record per refused submission is a
// table-growth vector; hence the per-window cap.

const maxCapturedRefusalsPerWindow = 20

const refusalWindow = time.Hour

type refusalCounter struct {
	mu     sync.Mutex
	seen   map[string]*refusalWindowState
	clock  func() time.Time
	window time.Duration
	cap    int
}

func (c *refusalCounter) windowLen() time.Duration {
	if c.window > 0 {
		return c.window
	}
	return refusalWindow
}

func (c *refusalCounter) capacity() int {
	if c.cap > 0 {
		return c.cap
	}
	return maxCapturedRefusalsPerWindow
}

type refusalWindowState struct {
	start    time.Time
	captured int
	dropped  int
	markedAt time.Time
}

var refusals = &refusalCounter{clock: time.Now}

func (c *refusalCounter) admit(key string) (capture bool, marker bool, dropped int) {
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]*refusalWindowState{}
	}
	st, ok := c.seen[key]
	if !ok || now.Sub(st.start) >= c.windowLen() {
		carried := 0
		if ok {
			carried = st.dropped
		}
		st = &refusalWindowState{start: now, dropped: carried}
		c.seen[key] = st
	}
	if st.captured < c.capacity() {
		st.captured++
		return true, false, 0
	}
	st.dropped++
	if st.markedAt.IsZero() || now.Sub(st.markedAt) >= c.windowLen() {
		st.markedAt = now
		n := st.dropped
		st.dropped = 0
		return false, true, n
	}
	return false, false, 0
}

func refusalCode(err error) string {
	switch {
	case errors.Is(err, core.ErrPlanLimit):
		return "plan_run_cap"
	case errors.Is(err, core.ErrOrgSuspended):
		return "org_suspended"
	case errors.Is(err, core.ErrGraphTooLarge):
		return "graph_too_large"
	case strings.Contains(err.Error(), "invalid graph"):
		return "invalid_graph"
	default:
		return "delivery_refused"
	}
}

func refusalMessage(err error) string {
	const kept = " The delivery is kept in this run — press Retry to process it."
	switch {
	case errors.Is(err, core.ErrPlanLimit):
		return "Refused: over the plan's monthly run limit." + kept
	case errors.Is(err, core.ErrOrgSuspended):
		return "Refused: this organisation is suspended." + kept
	case errors.Is(err, core.ErrGraphTooLarge):
		return "Refused: the flow has more steps than this plan allows." + kept
	case strings.Contains(err.Error(), "invalid graph"):
		return "Refused: the published flow no longer validates, so it cannot run. " +
			"Fix the flow and publish it." + kept
	default:
		return "Refused: " + err.Error() + kept
	}
}

func (s *Service) recordRefusedDelivery(
	ctx context.Context,
	g core.Graph,
	seeds map[string]core.Result,
	code, message string,
) (string, bool) {
	if s.Jobs == nil {
		return "", false
	}
	key := g.Tenant + "/" + g.Workspace + "/" + g.ID
	capture, marker, dropped := refusals.admit(key)
	switch {
	case capture:
	case marker:
		s.recordRefusalOverflow(ctx, g, dropped)
		return "", false
	default:
		return "", false
	}

	id, err := newID()
	if err != nil {
		return "", false
	}
	payload, err := json.Marshal(g)
	if err != nil {
		return "", false
	}
	rec := core.JobRecord{
		ID:           id,
		Kind:         core.JobKindGraph,
		GraphID:      g.ID,
		NodeID:       "*",
		Tenant:       g.Tenant,
		Workspace:    g.Workspace,
		Status:       core.JobStatusRunning,
		GraphPayload: payload,
		Job:          core.Job{ID: id, GraphID: g.ID},
	}
	if err := s.Jobs.Enqueue(ctx, rec); err != nil {
		s.logRefusal(g, fmt.Errorf("enqueue run: %w", err))
		return "", false
	}
	stored := true
	if errs := persistSeedsOnly(ctx, s.Jobs, g, id, seeds); len(errs) > 0 {
		stored = false
		for _, err := range errs {
			s.logRefusal(g, fmt.Errorf("store delivery: %w", err))
		}
	}
	result := &core.Result{
		JobID:  id,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: message},
	}
	if !stored {
		result.Error.Message = message +
			" The delivery itself could not be stored, so it cannot be retried."
	}
	if err := s.Jobs.Complete(ctx, id, core.JobStatusFailed, result); err != nil {
		s.logRefusal(g, fmt.Errorf("complete run: %w", err))
		return id, false
	}
	s.notifyRefusedDelivery(g, id, code, result.Error.Message)
	return id, stored
}

func (s *Service) recordRefusalOverflow(ctx context.Context, g core.Graph, dropped int) {
	id, err := newID()
	if err != nil {
		return
	}
	payload, _ := json.Marshal(core.Graph{
		ID: g.ID, Name: g.Name, Tenant: g.Tenant, Workspace: g.Workspace, Owner: g.Owner,
	})
	rec := core.JobRecord{
		ID:           id,
		Kind:         core.JobKindGraph,
		GraphID:      g.ID,
		NodeID:       "*",
		Tenant:       g.Tenant,
		Workspace:    g.Workspace,
		Status:       core.JobStatusRunning,
		GraphPayload: payload,
		Job:          core.Job{ID: id, GraphID: g.ID},
	}
	if err := s.Jobs.Enqueue(ctx, rec); err != nil {
		s.logRefusal(g, fmt.Errorf("enqueue overflow marker: %w", err))
		return
	}
	msg := fmt.Sprintf(
		"%d further deliveries were refused and NOT stored — too many in one hour to keep. Fix what is refusing them and the senders that retry will get through.",
		dropped)
	if err := s.Jobs.Complete(ctx, id, core.JobStatusFailed, &core.Result{
		JobID:  id,
		Status: core.StatusError,
		Error:  &core.JobError{Code: "deliveries_refused_not_stored", Message: msg},
	}); err != nil {
		s.logRefusal(g, fmt.Errorf("complete overflow marker: %w", err))
		return
	}
	s.notifyRefusedDelivery(g, id, "deliveries_refused_not_stored", msg)
}

func (s *Service) notifyRefusedDelivery(g core.Graph, runID, code, message string) {
	if s.Mailer == nil && (g.FailureNotify == nil || g.FailureNotify.Webhook == "") {
		return
	}
	payload := FailurePayload{
		GraphID:      g.ID,
		RunID:        runID,
		Tenant:       g.Tenant,
		Workspace:    g.Workspace,
		ErrorCode:    code,
		ErrorMessage: core.ClipNotificationText(message),
		FinishedAt:   time.Now().UTC().Format(time.RFC3339),
		RunURL:       buildRunURL(s.PublicBaseURL, g.Tenant, runID),
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.fireFailureNotification(ctx, g, payload); err != nil {
			s.logRefusal(g, fmt.Errorf("notify: %w", err))
		}
	}()
}

func (s *Service) logRefusal(g core.Graph, err error) {
	if s.Logger != nil {
		s.Logger.Printf("refused-delivery record [%s/%s/%s]: %v",
			g.Tenant, g.Workspace, g.ID, err)
	}
}

// A refused SCHEDULED fire is written as a terminal `skipped` run with no
// payload — nothing was lost, the trigger comes round again. A refused INBOUND
// delivery is written as `failed` WITH the payload, because someone typed
// something and it is gone unless it is kept. The asymmetry is the point:
// `skipped` means "didn't run", `failed` means "didn't run and something was
// lost".

func (s *Service) recordSkippedFire(ctx context.Context, tenant, workspace, graphID, code, message string) {
	if s.Jobs == nil {
		return
	}
	id, err := newID()
	if err != nil {
		return
	}
	payload, _ := json.Marshal(core.Graph{ID: graphID, Tenant: tenant, Workspace: workspace})
	rec := core.JobRecord{
		ID:           id,
		Kind:         core.JobKindGraph,
		GraphID:      graphID,
		NodeID:       "*",
		Tenant:       tenant,
		Workspace:    workspace,
		Status:       core.JobStatusRunning,
		GraphPayload: payload,
		Job:          core.Job{ID: id, GraphID: graphID},
	}
	if err := s.Jobs.Enqueue(ctx, rec); err != nil {
		if s.Logger != nil {
			s.Logger.Printf("skipped-run marker [%s/%s/%s]: enqueue: %v", tenant, workspace, graphID, err)
		}
		return
	}
	_ = s.Jobs.Complete(ctx, id, core.JobStatusSkipped, &core.Result{
		JobID:  id,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: message},
	})
}

func (s *Service) recordBrokenSchedule(ctx context.Context, g core.Graph, code, message string) {
	if s.Jobs == nil {
		return
	}
	id, err := newID()
	if err != nil {
		return
	}
	payload, err := json.Marshal(g)
	if err != nil {
		return
	}
	rec := core.JobRecord{
		ID:           id,
		Kind:         core.JobKindGraph,
		GraphID:      g.ID,
		NodeID:       "*",
		Tenant:       g.Tenant,
		Workspace:    g.Workspace,
		Status:       core.JobStatusRunning,
		GraphPayload: payload,
		Job:          core.Job{ID: id, GraphID: g.ID},
	}
	if err := s.Jobs.Enqueue(ctx, rec); err != nil {
		s.logRefusal(g, fmt.Errorf("enqueue broken-schedule marker: %w", err))
		return
	}
	if err := s.Jobs.Complete(ctx, id, core.JobStatusFailed, &core.Result{
		JobID:  id,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: message},
	}); err != nil {
		s.logRefusal(g, fmt.Errorf("complete broken-schedule marker: %w", err))
	}
}

func (s *Service) notifyRunCapReached(g core.Graph) {
	if s.Mailer == nil || s.Users == nil {
		return
	}
	if capture, _, _ := runCapMail.admit(g.Tenant); !capture {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		to := s.ownerFailureEmail(ctx, g)
		if to == "" {
			return
		}
		m := s.mailMsgs(ctx, to)
		content := emailtheme.Content{
			Subject:   m.RunCapSubject,
			Preheader: m.RunCapPreheader,
			Eyebrow:   m.RunCapEyebrow,
			Heading:   m.RunCapHeading,
			Tone:      "danger",
			Intro:     []string{m.RunCapIntro},
			Outro:     []string{m.RunCapOutro},
			LogoURL:   emailLogoURL(s.PublicBaseURL),
		}
		if s.PublicBaseURL != "" {
			content.Button = &emailtheme.Button{
				Label: m.RunCapButton,
				URL:   withOrg(strings.TrimRight(s.PublicBaseURL, "/")+"/settings/usage", g.Tenant),
			}
		}
		if err := s.Mailer.SendThemed(ctx, to, emailtheme.PlainText(content), content); err != nil {
			s.logRefusal(g, fmt.Errorf("run-cap email: %w", err))
		}
	}()
}

var runCapMail = &refusalCounter{clock: time.Now, window: runCapMailWindow, cap: 1}

const runCapMailWindow = 24 * time.Hour
