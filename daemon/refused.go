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

// Records for the fires and deliveries a submission gate refused, so a refusal
// is something the owner can find rather than a line in the daemon log.
//
// Two shapes, because two different things happen to the work:
//
//	recordSkippedFire      — a SCHEDULED fire that didn't happen. Nothing was
//	                         lost: the trigger comes round again on the next
//	                         tick. Written as a terminal `skipped` run,
//	                         coalesced by the caller, no payload.
//
//	recordRefusedDelivery  — an INBOUND delivery (hosted form, webhook) that
//	                         didn't run. Someone typed something, or a service
//	                         sent an event, and it is gone unless we keep it.
//	                         Written as a terminal `failed` run WITH the
//	                         payload, so it is visible, retryable and mailed
//	                         about.
//
// The asymmetry is the point. `skipped` means "didn't run"; `failed` means
// "didn't run and something was lost".

// maxCapturedRefusalsPerWindow bounds how many refused deliveries one flow
// stores per refusalWindow. A hosted form is an open door — anyone with the
// link can post to it — so a record per refused submission is a table-growth
// vector on a flow whose org is over its cap. Past the bound the payload is
// dropped and one marker per window says how many, which is the honest answer:
// we would rather tell an owner "42 more were refused and not kept" than
// pretend we kept them.
//
// Twenty is chosen so a real small-business form loses nothing (a contact form
// taking twenty submissions an hour while its org sits over the run cap is
// already an unusual hour) while a flood is bounded. The honeypot and the
// per-IP throttle on the route absorb most bot traffic upstream of here.
const maxCapturedRefusalsPerWindow = 20

// refusalWindow is the period maxCapturedRefusalsPerWindow applies over, and
// how often an over-bound flow writes its "and N more" marker.
const refusalWindow = time.Hour

// refusalCounter tracks captures per flow per window. In memory, in the same
// shape as the scheduler's skip coalescing: this sits on the inbound request
// path, so it must not cost a query, and a restart resetting it is worth at
// most one extra window's captures.
type refusalCounter struct {
	mu    sync.Mutex
	seen  map[string]*refusalWindowState
	clock func() time.Time
	// window overrides refusalWindow for counters measuring something else
	// (the run-cap email is per day). Zero means refusalWindow.
	window time.Duration
	// cap overrides maxCapturedRefusalsPerWindow. Zero means that default;
	// 1 makes the counter a plain "once per window" gate.
	cap int
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
	// dropped counts refusals past the bound since the last marker.
	dropped int
	// markedAt is when this flow last wrote an "and N more" marker.
	markedAt time.Time
}

var refusals = &refusalCounter{clock: time.Now}

// admit reports what to do with one refusal: capture it with its payload, or
// drop the payload and (when the window turns over) write a counting marker
// for everything dropped since the last one.
func (c *refusalCounter) admit(key string) (capture bool, marker bool, dropped int) {
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]*refusalWindowState{}
	}
	st, ok := c.seen[key]
	if !ok || now.Sub(st.start) >= c.windowLen() {
		// A fresh window keeps whatever the last one dropped, so the count in
		// the marker covers every refusal since the marker before it.
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

// refusalCode maps a submission gate's error to a stable code for the run
// record. Anything unrecognised is still recorded, under a generic code —
// a refusal we can't name is exactly the kind we want a record of.
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

// refusalMessage is what the owner reads in the Runs list and in the mail.
// Each one names the cause and the way out, because the recovery is not
// obvious: the delivery is sitting in this run, and Retry replays it once
// whatever refused it is fixed.
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

// recordRefusedDelivery persists an inbound delivery that a submission gate
// refused, as a terminal FAILED run carrying the delivery itself.
//
// Why failed rather than skipped: the payload is lost unless someone acts on
// it, and every mechanism that acts on a lost run is keyed to failure. The run
// lists as a failure; the run-detail page shows the submitted values; the
// account export includes it; and ResumeFailedRun already accepts a failed run
// and re-seeds from succeeded, inline node-records — which is exactly what the
// seeds written here are. So the recovery path needs no new code: the owner
// fixes what was refused (upgrades, un-suspends, repairs the flow), presses
// Retry, and the delivery processes.
//
// Returns the run ID and whether the payload was actually stored. A caller
// answering a machine should let that decide the status code: a captured
// delivery is ours now and must not be retried, an uncaptured one should be.
//
// Best-effort about its own failures, like recordSkippedFire: the delivery was
// already not going to run, and a store error here must not turn into a second
// error on top of the refusal.
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
		// Over the bound: say how many deliveries were refused and NOT kept,
		// so the gap in the record is stated rather than inferred.
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
	// Enqueued live, then completed: Complete is what stamps finished_at, and
	// retention reads that to decide when this run ages out.
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
	// The delivery itself, stored the way a deferred run stores one: a
	// succeeded node-record per seeded trigger node, outputs inline. This is
	// the whole point of the record — without it there is a note that
	// something arrived and no way to find out what.
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

// recordRefusalOverflow writes the "and N more" marker for a flow past the
// capture bound: a terminal failed run with no payload, whose message is the
// count. Same best-effort contract as the capture path.
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

// notifyRefusedDelivery mails/webhooks the owner about a refused delivery.
//
// Detached, because the caller is holding a visitor's HTTP request open: the
// notification does SMTP and an outbound POST with a 10s timeout each, and a
// form's confirmation page must not wait on either. Bounded so a wedged mail
// host can't leak goroutines.
//
// Routed through the ordinary failure-notification path, so the per-flow hourly
// throttle covers it: a flow whose form is refusing every submission sends one
// mail, not one per visitor. That the run is already in the store before this
// runs is what makes the throttle see it.
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

// recordSkippedFire writes a terminal "skipped" graph run so a cap-blocked
// scheduled fire is visible in the Runs list, not just the server log. It
// enqueues a bare graph record (no node work, so nothing dispatches) and
// immediately completes it skipped with the reason. Best-effort: a write
// failure is logged, never propagated — the fire already wasn't going to run.
// The scheduler coalesces calls (one per flow per window); the precise count
// lives in the usage counter.
//
// No payload and no notification, unlike recordRefusedDelivery above: a
// scheduled fire that didn't happen lost nothing to keep, and the next tick
// will try again.
func (s *Service) recordSkippedFire(ctx context.Context, tenant, workspace, graphID, code, message string) {
	if s.Jobs == nil {
		return
	}
	id, err := newID()
	if err != nil {
		return
	}
	// A minimal (node-less) graph payload so the run-detail view renders
	// safely rather than choking on an empty payload.
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

// recordBrokenSchedule records a scheduled fire that did NOT happen because
// the flow itself could not be reached or read — its workspace would not open,
// its published revision would not load, or the submission gate refused it for
// something the owner has to fix.
//
// Written FAILED rather than skipped, which is the difference that matters:
// skipped means "did not run, nothing lost, the next tick will try again", and
// that is true of a plan-cap skip. This is not that. A flow whose published
// revision no longer loads has STOPPED WORKING, silently, and every one of
// these paths used to be a single line in the daemon log — no run record, no
// marker, nothing in the Runs list. The owner's daily report simply stopped
// arriving and the first sign was somebody downstream asking where it was.
// Failed puts it in the Runs list and hands it to the notification sweep, so
// it reaches a person.
//
// The caller coalesces (see Scheduler.markOnce): a per-minute cron that cannot
// load its flow would otherwise write 1440 identical records a day.
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
	// No direct notify call: the record is failed, so SweepFailureNotifications
	// picks it up with the same throttle and escalation as any other failure.
}

// notifyRunCapReached tells the flow's owner that the organisation has run out
// of its monthly allowance and its scheduled flows have stopped firing.
//
// Nothing told them before. The signals were an in-app Usage banner and a
// coalesced marker in the Runs list — both of which require somebody to be
// looking at the app, which is precisely what an automation product's users
// are not doing. Their flows stop, everything looks calm, and they find out
// from a customer.
//
// Coalesced per ORGANISATION, not per flow: a tenant over its cap has every
// scheduled flow skipping at once, so per-flow mail would be a storm about a
// single fact. The owner of whichever flow trips it first is the recipient —
// they are a person who cares, and resolving it (upgrading) is org-wide.
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

// runCapMail coalesces the run-cap email to one per organisation per day. A
// day rather than an hour because the fact does not change until somebody
// upgrades, and the calendar month is the window it is about.
var runCapMail = &refusalCounter{clock: time.Now, window: runCapMailWindow, cap: 1}

// runCapMailWindow is refusalWindow's equivalent for the run-cap email.
const runCapMailWindow = 24 * time.Hour
