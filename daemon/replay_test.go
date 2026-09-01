// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

func replayPrincipal() core.Principal {
	return core.Principal{
		Subject: "u", Tenant: "acme", Workspace: "ws1",
		Roles: []core.Role{{Name: "editor", Permissions: []core.Permission{
			core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
		}}},
	}
}

// fireWebhook posts a body to a flow's trigger URL through the real listener
// and returns the run it started.
func fireWebhook(t *testing.T, wh *daemon.WebhookListener, flowID, secret, body string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		callPrivateHandler(t, wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/"+flowID, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST trigger: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("trigger status = %d, want 202", resp.StatusCode)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode trigger response: %v", err)
	}
	return out.JobID
}

// TestReplayRun_ResendsWebhookBody is the bug this endpoint exists for:
// re-running a webhook-triggered run used to submit the flow afresh, which
// left the webhook step with nothing and killed the whole re-run on its first
// step ("nothing was sent to this flow"). The replay must hand the new run the
// body the original delivery carried.
func TestReplayRun_ResendsWebhookBody(t *testing.T) {
	svc, wh, jobs, bus, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "wh-replay", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s3cr3t"}}},
			{ID: "after", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "after", ToPort: "in"}},
	}
	savePublished(t, wsStore, g)

	run1 := fireWebhook(t, wh, "wh-replay", "s3cr3t", `{"event":"hello"}`)
	if term := waitForTerminalEvent(t, bus, jobs, run1, 5*time.Second); term.Status != core.JobStatusSucceeded {
		t.Fatalf("run1 status = %q, want succeeded (err=%+v)", term.Status, term.Error)
	}
	orig, err := jobs.Get(t.Context(), daemon.NodeJobID(run1, "in"))
	if err != nil {
		t.Fatalf("get original webhook record: %v", err)
	}

	run2, err := svc.ReplayRun(t.Context(), replayPrincipal(), run1)
	if err != nil {
		t.Fatalf("ReplayRun: %v", err)
	}
	if run2 == run1 {
		t.Fatalf("replay returned the same run id %q, want a new run", run2)
	}
	if term := waitForTerminalEvent(t, bus, jobs, run2, 5*time.Second); term.Status != core.JobStatusSucceeded {
		t.Fatalf("replayed run status = %q, want succeeded (err=%+v) — the webhook step got no data", term.Status, term.Error)
	}

	rec, err := jobs.Get(t.Context(), daemon.NodeJobID(run2, "in"))
	if err != nil {
		t.Fatalf("get replayed webhook record: %v", err)
	}
	if rec.Status != core.JobStatusSucceeded {
		t.Fatalf("replayed webhook step status = %q, want succeeded", rec.Status)
	}
	if rec.Result == nil || orig.Result == nil {
		t.Fatalf("missing results: original=%v replayed=%v", orig.Result, rec.Result)
	}
	if got, want := rec.Result.Output["body"].Inline, orig.Result.Output["body"].Inline; !reflect.DeepEqual(got, want) {
		t.Errorf("replayed body = %#v, want the original delivery %#v", got, want)
	}
	if got := rec.Result.Output["headers"].Inline; got == nil {
		t.Errorf("replayed headers = nil, want the original request's headers")
	}
	// The seed must be re-addressed to the NEW run, not carry run1's job id.
	if want := daemon.NodeJobID(run2, "in"); rec.Result.JobID != want {
		t.Errorf("replayed result JobID = %q, want %q", rec.Result.JobID, want)
	}
}

// TestReplayRun_RefusesRunWithNoDelivery: a webhook flow someone ran by hand
// received nothing, so there is nothing to replay. Refuse with the sentinel
// the gateway turns into an actionable message rather than starting a run that
// is certain to die on its first step.
func TestReplayRun_RefusesRunWithNoDelivery(t *testing.T) {
	svc, _, jobs, bus, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "wh-manual", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s3cr3t"}}},
		},
	}
	savePublished(t, wsStore, g)
	p := replayPrincipal()

	run1, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("submit manual run: %v", err)
	}
	if term := waitForTerminalEvent(t, bus, jobs, run1, 5*time.Second); term.Status != core.JobStatusFailed {
		t.Fatalf("manual run of a webhook flow status = %q, want failed", term.Status)
	}

	if _, err := svc.ReplayRun(t.Context(), p, run1); !errors.Is(err, daemon.ErrReplayNoTriggerData) {
		t.Fatalf("ReplayRun error = %v, want ErrReplayNoTriggerData", err)
	}
	if _, err := svc.ReplayRun(t.Context(), p, run1); !errors.Is(err, core.ErrConflict) {
		t.Errorf("refusal must wrap core.ErrConflict so the gateway answers 409")
	}
}

// TestReplayRun_NoTriggerFlowJustRunsAgain: a flow with no inbound trigger has
// no delivery to reuse, and must NOT be refused — replay is a plain re-run.
func TestReplayRun_NoTriggerFlowJustRunsAgain(t *testing.T) {
	svc, _, jobs, bus, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "plain", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	savePublished(t, wsStore, g)
	p := replayPrincipal()

	run1, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTerminalEvent(t, bus, jobs, run1, 5*time.Second)

	run2, err := svc.ReplayRun(t.Context(), p, run1)
	if err != nil {
		t.Fatalf("ReplayRun on a trigger-less flow: %v", err)
	}
	if term := waitForTerminalEvent(t, bus, jobs, run2, 5*time.Second); term.Status != core.JobStatusSucceeded {
		t.Fatalf("replayed run status = %q, want succeeded", term.Status)
	}
}

// TestReplayRun_RefusesWhenTriggerStepIsOff: seeding pre-completes a step and
// bypasses the worker, so seeding a paused trigger step would quietly run it —
// and NOT seeding it leaves the delivery nowhere to go. Refuse, the way the
// /trigger endpoint refuses a delivery to a paused step.
func TestReplayRun_RefusesWhenTriggerStepIsOff(t *testing.T) {
	svc, wh, jobs, bus, wsStore := startWebhookHarness(t)
	g := core.Graph{
		ID: "wh-off", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "in", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s3cr3t"}}},
		},
	}
	savePublished(t, wsStore, g)

	run1 := fireWebhook(t, wh, "wh-off", "s3cr3t", `{"event":"hello"}`)
	if term := waitForTerminalEvent(t, bus, jobs, run1, 5*time.Second); term.Status != core.JobStatusSucceeded {
		t.Fatalf("run1 status = %q, want succeeded", term.Status)
	}

	// The owner pauses the webhook step after the delivery landed.
	off := g
	off.Nodes = []core.Node{{ID: "in", Module: "webhook_input", Disabled: true,
		Params: map[string]any{"secrets": []any{"s3cr3t"}}}}
	savePublished(t, wsStore, off)

	if _, err := svc.ReplayRun(t.Context(), replayPrincipal(), run1); !errors.Is(err, daemon.ErrReplayTriggerOff) {
		t.Fatalf("ReplayRun error = %v, want ErrReplayTriggerOff", err)
	}
}
