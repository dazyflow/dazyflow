// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
)

var (
	// ErrReplayNoTriggerData: the run being replayed never received an inbound
	// delivery (it was started by hand, or its trigger step failed), so there
	// is nothing to re-send.
	ErrReplayNoTriggerData  = fmt.Errorf("%w: this run wasn't started by an incoming webhook or form submission, so there is no delivery to re-send", core.ErrConflict)
	ErrReplayTriggerChanged = fmt.Errorf("%w: this flow's trigger step has changed since this run, so its original delivery can no longer be matched to it", core.ErrConflict)
	ErrReplayTriggerOff     = fmt.Errorf("%w: this flow's trigger step is turned off — turn the step back on to re-send what it received", core.ErrConflict)
)

// ReplayRun re-runs a finished run from the start, feeding the flow's trigger
// step the data the original run was started with. Returns the new run's ID.
//
// It needs its own entry point rather than "submit the flow again" because a
// flow started by an inbound delivery begins at a step whose data arrived WITH
// that request. Nothing re-derives it — the trigger drops' Execute deliberately
// errors with no_trigger_data — so re-submitting produced a run that died on its
// first step.
//
// The delivery is still on record: the trigger path pre-completes the trigger
// node with the request body and headers as its result, so the original run's
// node record holds that exact payload. Replay re-seeds it and lets every other
// step execute normally — side effects included, which is why the UI gates the
// button behind a confirm.
//
// The flow definition is the CURRENT one, matching the editor's Run button: the
// point of replaying a failed delivery is usually "I fixed the flow, put that
// payload through it again". Only the trigger data comes from the old run.
func (s *Service) ReplayRun(ctx context.Context, p core.Principal, runID string) (string, error) {
	rec, err := s.GetJob(ctx, p, runID)
	if err != nil {
		return "", err
	}
	if rec.Kind != core.JobKindGraph {
		return "", fmt.Errorf("%w: %q is a node record, not a run", core.ErrNotFound, runID)
	}
	g, err := s.replayGraph(ctx, p, rec)
	if err != nil {
		return "", err
	}
	seeds, err := s.replayTriggerSeeds(ctx, rec, g)
	if err != nil {
		return "", err
	}
	// Manual: a person pressed Replay and is watching the run they navigate
	// to, so no failure email — same reasoning as retry (see resume.go).
	return s.SubmitGraphOpts(ctx, p, g, SubmitOpts{Seeds: seeds, Manual: true})
}

// replayGraph resolves which graph a replay executes: the flow's current
// definition when it still exists, else the revision stored on the run.
//
// The fallback is what keeps the run page's Replay honest for a run whose flow
// was since deleted or renamed — the run record carries the exact graph it
// executed. A refusal we must NOT paper over this way is authorization: if the
// caller may not read the flow, replaying its stored copy is the same breach.
func (s *Service) replayGraph(ctx context.Context, p core.Principal, rec core.JobRecord) (core.Graph, error) {
	g, err := s.LoadGraph(ctx, p, rec.Tenant, rec.Workspace, rec.GraphID, "")
	if err == nil {
		return g, nil
	}
	if errors.Is(err, core.ErrUnauthorized) || len(rec.GraphPayload) == 0 {
		return core.Graph{}, err
	}
	var stored core.Graph
	if uerr := json.Unmarshal(rec.GraphPayload, &stored); uerr != nil {
		return core.Graph{}, fmt.Errorf("decode stored graph for run %q: %w", rec.ID, uerr)
	}
	return stored, nil
}

// replayTriggerSeeds builds the seed map that hands the new run the old run's
// inbound delivery: for every live inbound-event trigger step in the graph, the
// result its node record holds in the original run.
//
// Returns nil (a plain re-run, no seeds) when the flow has no such trigger —
// a cron/poll flow re-derives its own data, and a flow with no trigger at all
// is just a manual flow. When the flow DOES start from a delivery but this run
// carries none we can reuse, the replay is refused rather than started:
// running it would burn a run to fail on its first step with "nothing was sent
// to this flow", which is exactly the bug this path exists to fix.
func (s *Service) replayTriggerSeeds(
	ctx context.Context,
	rec core.JobRecord,
	g core.Graph,
) (map[string]core.Result, error) {
	// live are the trigger steps a seed may go to. A step that is turned OFF is
	// not one of them: seeding pre-completes the record and bypasses the worker
	// entirely, so seeding a paused step would quietly run it. With every
	// trigger step off there is nowhere to put the delivery, and the run would
	// skip that step and everything downstream — so say so, the way the
	// /trigger endpoint refuses a delivery to a paused step.
	live := map[string]struct{}{}
	present := 0
	for _, n := range g.Nodes {
		if !core.IsInboundEventTriggerModule(n.Module) {
			continue
		}
		present++
		if !triggerNodeDisabled(n) {
			live[n.ID] = struct{}{}
		}
	}
	if len(live) == 0 {
		if present > 0 {
			return nil, ErrReplayTriggerOff
		}
		return nil, nil
	}

	nodes, err := s.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant:     rec.Tenant,
		Workspace:  rec.Workspace,
		GraphRunID: rec.ID,
		Limit:      5000,
	})
	if err != nil {
		return nil, fmt.Errorf("list node records for run %q: %w", rec.ID, err)
	}

	wasTrigger := inboundTriggerNodes(g)
	if len(rec.GraphPayload) > 0 {
		var ran core.Graph
		if json.Unmarshal(rec.GraphPayload, &ran) == nil {
			wasTrigger = inboundTriggerNodes(ran)
		}
	}

	seeds := map[string]core.Result{}
	delivered := 0
	for _, n := range nodes {
		if _, ok := wasTrigger[n.NodeID]; !ok {
			continue
		}
		if n.Status != core.JobStatusSucceeded || n.Result == nil || !outputsReusable(n.Result) {
			continue
		}
		delivered++
		if _, isLive := live[n.NodeID]; isLive {
			seeds[n.NodeID] = *n.Result
		}
	}
	if len(seeds) == 0 {
		if delivered == 0 {
			return nil, ErrReplayNoTriggerData
		}
		return nil, ErrReplayTriggerChanged
	}
	return seeds, nil
}

func inboundTriggerNodes(g core.Graph) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, n := range g.Nodes {
		if core.IsInboundEventTriggerModule(n.Module) {
			ids[n.ID] = struct{}{}
		}
	}
	return ids
}
