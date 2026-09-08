// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

func refusedHarness(t *testing.T) *Service {
	t.Helper()
	h := newGatewayHarness(t)
	refusals = &refusalCounter{clock: time.Now}
	t.Cleanup(func() { refusals = &refusalCounter{clock: time.Now} })
	return h.svc
}

func refusedGraph() core.Graph {
	return core.Graph{
		ID: "contact", Name: "Contact form", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "in", Module: core.FormInputModule}},
	}
}

func submission(values map[string]any) map[string]core.Result {
	return map[string]core.Result{
		"in": {Status: core.StatusOK, Output: map[string]core.Ref{
			"body": {MIME: "application/json", Inline: values},
		}},
	}
}

// A refused delivery has to survive as something the owner can find, read and
// act on. This pins all three: the run is terminal and failed (so it lists as
// a failure and the retry path accepts it), the reason is legible, and the
// visitor's actual answers are inside it.
func TestRecordRefusedDelivery_KeepsTheSubmission(t *testing.T) {
	svc := refusedHarness(t)
	g := refusedGraph()
	seeds := submission(map[string]any{"email": "someone@example.com", "message": "hello"})

	runID, stored := svc.recordRefusedDelivery(t.Context(), g, seeds,
		refusalCode(core.ErrPlanLimit), refusalMessage(core.ErrPlanLimit))
	if runID == "" || !stored {
		t.Fatalf("recordRefusedDelivery = %q, stored=%v; want a run ID and stored", runID, stored)
	}

	rec, err := svc.Jobs.Get(t.Context(), runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if rec.Status != core.JobStatusFailed {
		t.Errorf("status = %q, want failed (skipped would not be retryable and would not notify)", rec.Status)
	}
	if rec.FinishedAt == nil {
		t.Error("finished_at unset: retention keys the run's age on it")
	}
	if rec.Result == nil || rec.Result.Error == nil {
		t.Fatal("no error on the run: nothing tells the owner why")
	}
	if rec.Result.Error.Code != "plan_run_cap" {
		t.Errorf("code = %q, want plan_run_cap", rec.Result.Error.Code)
	}
	if len(rec.GraphPayload) == 0 {
		t.Error("no graph payload: the run-detail view has nothing to render")
	}

	seedRec, err := svc.Jobs.Get(t.Context(), NodeJobID(runID, "in"))
	if err != nil {
		t.Fatalf("get seed record: %v", err)
	}
	if seedRec.Status != core.JobStatusSucceeded {
		t.Errorf("seed status = %q, want succeeded (retry re-seeds from succeeded records)", seedRec.Status)
	}
	body, ok := seedRec.Result.Output["body"]
	if !ok {
		t.Fatal("seed record has no body output")
	}
	values, _ := body.Inline.(map[string]any)
	if values["email"] != "someone@example.com" {
		t.Errorf("submitted values not preserved: %#v", body.Inline)
	}
	if body.Ref != "" {
		t.Error("body stored by reference; retry only re-seeds inline outputs")
	}
}

// The whole point of recording it as failed-with-payload is that Retry
// replays the delivery once the owner fixes what refused it. That contract
// lives in ResumeFailedRun, so assert it end to end rather than trusting the
// status alone.
func TestRecordRefusedDelivery_IsRetryable(t *testing.T) {
	svc := refusedHarness(t)
	g := refusedGraph()
	seeds := submission(map[string]any{"email": "someone@example.com"})

	runID, _ := svc.recordRefusedDelivery(t.Context(), g, seeds,
		refusalCode(core.ErrPlanLimit), refusalMessage(core.ErrPlanLimit))

	p := SystemPrincipal("test", "t", "ws")
	retryID, err := svc.ResumeFailedRun(t.Context(), p, runID)
	if err != nil {
		t.Fatalf("retry a refused delivery: %v", err)
	}
	if retryID == "" || retryID == runID {
		t.Fatalf("retry produced no new run (%q)", retryID)
	}
	// The retry must carry the original submission forward, not an empty form.
	seedRec, err := svc.Jobs.Get(t.Context(), NodeJobID(retryID, "in"))
	if err != nil {
		t.Fatalf("retry lost the seed: %v", err)
	}
	body := seedRec.Result.Output["body"]
	values, _ := body.Inline.(map[string]any)
	if values["email"] != "someone@example.com" {
		t.Errorf("retry did not replay the submission: %#v", body.Inline)
	}
}

// A hosted form is an open door, so the capture has to be bounded. Past the
// bound the payload is dropped — but the owner must still be told that it was,
// rather than being left with a record that looks complete.
func TestRecordRefusedDelivery_BoundedThenSaysSo(t *testing.T) {
	svc := refusedHarness(t)
	g := refusedGraph()
	code, msg := refusalCode(core.ErrPlanLimit), refusalMessage(core.ErrPlanLimit)

	captured := 0
	for i := 0; i < maxCapturedRefusalsPerWindow+5; i++ {
		if _, stored := svc.recordRefusedDelivery(t.Context(), g,
			submission(map[string]any{"n": i}), code, msg); stored {
			captured++
		}
	}
	if captured != maxCapturedRefusalsPerWindow {
		t.Errorf("captured %d submissions, want the bound of %d", captured, maxCapturedRefusalsPerWindow)
	}

	runs, err := core.ListRunSummaries(t.Context(), svc.Jobs, core.ListGraphRunsOpts{
		Tenant: "t", Workspace: "ws", GraphID: "contact", Limit: 200,
	})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	// The bound's worth of captures, plus exactly one marker for the overflow.
	overflow := 0
	for _, r := range runs {
		if r.Error != nil && r.Error.Code == "deliveries_refused_not_stored" {
			overflow++
		}
	}
	if overflow != 1 {
		t.Errorf("overflow markers = %d, want exactly 1 (coalesced)", overflow)
	}
	if len(runs) != maxCapturedRefusalsPerWindow+1 {
		t.Errorf("recorded %d runs, want %d captures + 1 marker",
			len(runs), maxCapturedRefusalsPerWindow)
	}
}

// A window turning over lets the flow capture again, and the marker it writes
// then accounts for everything dropped since the previous marker — the count
// must not silently reset to zero.
func TestRefusalCounter_WindowTurnover(t *testing.T) {
	now := time.Now()
	c := &refusalCounter{clock: func() time.Time { return now }}

	for i := 0; i < maxCapturedRefusalsPerWindow; i++ {
		if capture, _, _ := c.admit("f"); !capture {
			t.Fatalf("refusal %d should have been captured", i)
		}
	}
	capture, marker, dropped := c.admit("f")
	if capture || !marker || dropped != 1 {
		t.Fatalf("first overflow = capture %v, marker %v, dropped %d; want false, true, 1",
			capture, marker, dropped)
	}
	for i := 0; i < 4; i++ {
		if capture, marker, _ := c.admit("f"); capture || marker {
			t.Fatalf("overflow %d should be silent: capture %v, marker %v", i, capture, marker)
		}
	}
	now = now.Add(refusalWindow + time.Second)
	for i := 0; i < maxCapturedRefusalsPerWindow; i++ {
		if capture, _, _ := c.admit("f"); !capture {
			t.Fatalf("new window: refusal %d should have been captured", i)
		}
	}
	// The new window's first overflow writes its own marker, and that marker
	// has to account for the 4 the previous window dropped in silence as well
	// as this one — otherwise those 4 are never reported to anybody.
	_, marker, dropped = c.admit("f")
	if !marker {
		t.Fatal("second window should write its own marker")
	}
	if dropped != 5 {
		t.Errorf("marker reported %d dropped, want 5 (4 carried + this one)", dropped)
	}
}

// Two flows must not share one flow's bound.
func TestRefusalCounter_PerFlow(t *testing.T) {
	now := time.Now()
	c := &refusalCounter{clock: func() time.Time { return now }}
	for i := 0; i < maxCapturedRefusalsPerWindow; i++ {
		c.admit("flow-a")
	}
	if capture, _, _ := c.admit("flow-b"); !capture {
		t.Error("flow-b was refused capture because flow-a hit the bound")
	}
}

// The scheduler's skipped-fire marker keeps its own contract: a scheduled fire
// that didn't happen lost nothing, so it stays `skipped`, carries no payload,
// and must not start looking like a lost delivery.
func TestRecordSkippedFire_StaysSkippedAndPayloadFree(t *testing.T) {
	svc := refusedHarness(t)
	svc.recordSkippedFire(t.Context(), "t", "ws", "nightly", "plan_run_cap",
		"Scheduled run skipped — over the plan's monthly run limit.")

	runs, err := core.ListRunSummaries(t.Context(), svc.Jobs, core.ListGraphRunsOpts{
		Tenant: "t", Workspace: "ws", GraphID: "nightly", Limit: 10,
	})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].Status != core.JobStatusSkipped {
		t.Errorf("status = %q, want skipped", runs[0].Status)
	}
	nodes, err := svc.Jobs.ListNodeRecords(t.Context(), core.ListNodeRecordsOpts{
		Tenant: "t", Workspace: "ws", GraphRunID: runs[0].ID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list node records: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("skipped marker carries %d node record(s); it should carry none", len(nodes))
	}
}

func TestRefusalCode_And_Message(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{core.ErrPlanLimit, "plan_run_cap"},
		{core.ErrOrgSuspended, "org_suspended"},
		{core.ErrGraphTooLarge, "graph_too_large"},
		{errors.New("invalid graph: two wires into one input"), "invalid_graph"},
		{errors.New("something nobody has seen"), "delivery_refused"},
	}
	for _, tc := range cases {
		if got := refusalCode(tc.err); got != tc.code {
			t.Errorf("refusalCode(%v) = %q, want %q", tc.err, got, tc.code)
		}
		if msg := refusalMessage(tc.err); msg == "" || !strings.Contains(msg, "Retry") {
			t.Errorf("refusalMessage(%v) = %q; want it to name the way out", tc.err, msg)
		}
	}
}

func TestNotifyRunCapReached_MailsTheOwnerOncePerOrg(t *testing.T) {
	svc := refusedHarness(t)
	srv := attachOwnerEmail(t, svc, auth.User{Email: "owner@example.com"})
	runCapMail = &refusalCounter{clock: time.Now, window: runCapMailWindow, cap: 1}
	t.Cleanup(func() {
		runCapMail = &refusalCounter{clock: time.Now, window: runCapMailWindow, cap: 1}
	})
	svc.PublicBaseURL = "https://app.example.com"

	g := core.Graph{
		ID: "daily", Name: "Daily report", Tenant: "t", Workspace: "ws",
		Owner: "owner@example.com",
	}
	svc.notifyRunCapReached(g)

	data, to := waitForEmail(t, srv, 2*time.Second)
	if data == "" {
		t.Fatal("hitting the run cap sent no email")
	}
	if !strings.Contains(strings.Join(to, ","), "owner@example.com") {
		t.Errorf("email went to %v", to)
	}
	if !strings.Contains(strings.ToLower(data), "stopped running") {
		t.Errorf("email does not say the flows have stopped:\n%s", data)
	}

	// Coalesced per organisation: a tenant over its cap has every scheduled
	// flow skipping at once, so per-flow mail would be a storm about one fact.
	other := core.Graph{
		ID: "weekly", Tenant: "t", Workspace: "ws", Owner: "owner@example.com",
	}
	svc.notifyRunCapReached(other)
	svc.notifyRunCapReached(g)
	time.Sleep(300 * time.Millisecond)
	if _, _, again, _ := srv.snapshot(); again != data {
		t.Error("a second flow hitting the same org's cap sent another email")
	}
}
