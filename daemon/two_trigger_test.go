// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"

	_ "github.com/dazyflow/dazyflow/drops" // real trigger drops
)

// A flow can carry two triggers, and a run enters through exactly one of them.
// The others used to be dispatched anyway, because every root with no incoming
// edge is dispatched and only the SEEDED node is held back: the Slack branch
// ran on a webhook delivery and failed on its "no trigger data" sentinel, which
// failed the whole run even though the branch that did fire was fine.
func twoTriggerGraph(id string) core.Graph {
	return core.Graph{
		ID: id, Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "mention", Module: "slack_on_mention"},
			{ID: "relay", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "hook", Module: "webhook_input"},
			{ID: "forward", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "mention", FromPort: "text", To: "relay", ToPort: "pass"},
			{From: "hook", FromPort: "body", To: "forward", ToPort: "pass"},
		},
	}
}

func assertStatuses(t *testing.T, jobs core.JobStore, graphRunID string, want map[string]core.JobStatus) {
	t.Helper()
	for nodeID, status := range want {
		rec, err := jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, nodeID))
		if err != nil {
			t.Errorf("%s: %v", nodeID, err)
			continue
		}
		if rec.Status != status {
			t.Errorf("%s status = %q, want %q", nodeID, rec.Status, status)
		}
	}
}

func TestTwoTriggers_OnlyTheFiredBranchRuns(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	graphRunID, err := h.svc.SubmitGraphWithSeed(t.Context(), h.principal, twoTriggerGraph("two-trigger"),
		map[string]core.Result{
			"mention": {
				Status: core.StatusOK,
				Output: map[string]core.Ref{
					"text":    {MIME: "text/plain", Inline: "@dazyflow run it"},
					"channel": {MIME: "text/plain", Inline: "C024BE91L"},
				},
			},
		})
	if err != nil {
		t.Fatalf("SubmitGraphWithSeed: %v", err)
	}

	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (err=%+v)", terminal.Status, terminal.Error)
	}
	assertStatuses(t, h.jobs, graphRunID, map[string]core.JobStatus{
		"mention": core.JobStatusSucceeded, // seeded by the delivery
		"relay":   core.JobStatusSucceeded,
		"hook":    core.JobStatusSkipped, // no webhook call happened
		"forward": core.JobStatusSkipped, // so its branch is dormant, not failed
	})
	// The card has to be able to say WHY each one went grey, and the two
	// reasons are different: the trigger did not fire, the step after it was
	// never reached.
	assertSkipCodes(t, h.jobs, graphRunID, map[string]string{
		"hook":    core.SkipCodeTriggerNotFired,
		"forward": core.SkipCodeUpstream,
	})
}

// A skip is not a failure, and everything that reads Result.Error presents one
// as a failure — the run detail draws a red chip from its code alone. So the
// reason travels in its own field and Error stays empty.
func assertSkipCodes(t *testing.T, jobs core.JobStore, graphRunID string, want map[string]string) {
	t.Helper()
	for nodeID, code := range want {
		rec, err := jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, nodeID))
		if err != nil {
			t.Errorf("%s: %v", nodeID, err)
			continue
		}
		if rec.Result == nil {
			t.Errorf("%s carries no result, so nothing can explain the skip", nodeID)
			continue
		}
		if rec.Result.SkipCode != code {
			t.Errorf("%s skip code = %q, want %q", nodeID, rec.Result.SkipCode, code)
		}
		if rec.Result.Error != nil {
			t.Errorf("%s skip carries an error (%+v) — it would render as a failure",
				nodeID, rec.Result.Error)
		}
	}
}

// The other reason a step is skipped, and the one the card must NOT chip twice:
// a switched-off step already says so on its face.
func TestDisabledStep_CarriesItsOwnSkipCode(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	g := core.Graph{
		ID: "off-code", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "source"},
			{ID: "off", Module: "delay", Params: map[string]any{"ms": 1}, Disabled: true},
			{ID: "tail", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "src", FromPort: "out", To: "off", ToPort: "pass"},
			{From: "off", FromPort: "pass", To: "tail", ToPort: "pass"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("SubmitGraph: %v", err)
	}
	if terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second); terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded", terminal.Status)
	}
	assertSkipCodes(t, h.jobs, graphRunID, map[string]string{
		"off":  core.SkipCodeStepOff,
		"tail": core.SkipCodeUpstream,
	})
}

// The sentinel is the whole point of a manual Run on a trigger that carries no
// data: it tells the author the step needs a real firing. Nothing fired here,
// so nothing may be skipped — otherwise the flow would look like it ran.
func TestManualRun_KeepsTheNoTriggerDataSentinel(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, twoTriggerGraph("manual-two-trigger"))
	if err != nil {
		t.Fatalf("SubmitGraph: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("graph status = %q, want failed — neither trigger has data", terminal.Status)
	}
	rec, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "mention"))
	if err != nil {
		t.Fatalf("get mention: %v", err)
	}
	if rec.Status != core.JobStatusFailed {
		t.Errorf("mention status = %q, want failed (the sentinel), not skipped", rec.Status)
	}
	if rec.Result == nil || rec.Result.Error == nil || rec.Result.Error.Code != "no_trigger_data" {
		t.Errorf("mention error = %+v, want the no_trigger_data sentinel", rec.Result)
	}
}

// A scheduled run seeds nothing — cron derives its own fire moment — so the
// webhook step in the same flow used to execute standalone and fail the run on
// every single fire. It has no delivery, and the schedule is a way in that does
// not involve it, so its branch is dormant rather than broken.
func TestScheduledRun_SkipsTheWebhookBranch(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	g := core.Graph{
		ID: "cron-and-hook", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "clock", Module: "cron_trigger", Params: map[string]any{"cron": "0 9 * * *"}},
			{ID: "report", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "hook", Module: "webhook_input"},
			{ID: "forward", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "clock", FromPort: "fired_at", To: "report", ToPort: "pass"},
			{From: "hook", FromPort: "body", To: "forward", ToPort: "pass"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("SubmitGraph: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (err=%+v)", terminal.Status, terminal.Error)
	}
	assertStatuses(t, h.jobs, graphRunID, map[string]core.JobStatus{
		"clock":   core.JobStatusSucceeded,
		"report":  core.JobStatusSucceeded,
		"hook":    core.JobStatusSkipped,
		"forward": core.JobStatusSkipped,
	})
}

// The mirror image, and the reason the rule cannot simply be "another trigger
// succeeded": cron_trigger succeeds standalone, so on a delivery-started run it
// would otherwise stamp a fire moment nothing scheduled.
func TestDelivery_SkipsTheScheduleBranch(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	g := core.Graph{
		ID: "hook-and-cron", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "hook", Module: "webhook_input"},
			{ID: "forward", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "clock", Module: "cron_trigger", Params: map[string]any{"cron": "0 9 * * *"}},
			{ID: "report", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "hook", FromPort: "body", To: "forward", ToPort: "pass"},
			{From: "clock", FromPort: "fired_at", To: "report", ToPort: "pass"},
		},
	}
	graphRunID, err := h.svc.SubmitGraphWithSeed(t.Context(), h.principal, g,
		map[string]core.Result{
			"hook": {
				Status: core.StatusOK,
				Output: map[string]core.Ref{"body": {MIME: "application/json", Inline: map[string]any{"order": "4471"}}},
			},
		})
	if err != nil {
		t.Fatalf("SubmitGraphWithSeed: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (err=%+v)", terminal.Status, terminal.Error)
	}
	assertStatuses(t, h.jobs, graphRunID, map[string]core.JobStatus{
		"hook":    core.JobStatusSucceeded,
		"forward": core.JobStatusSucceeded,
		"clock":   core.JobStatusSkipped,
		"report":  core.JobStatusSkipped,
	})
}
