// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/dazyflow/dazyflow/core"
)

// ResumeGraphRun continues a run paused at a breakpoint (#12).
//
//   - step=false (Continue): run proceeds until the next breakpoint or
//     completion.
//   - step=true (Step): advance one layer, then pause again after the next
//     node(s) regardless of breakpoints (step mode stays on until Continue).
//
// Mirrors CancelGraphRun's auth/setup and re-enters the dispatcher exactly
// as Service.Approve does after a human resume: it dispatches the dependents
// the breakpoint held back. Returns ErrConflict if the run isn't paused.
func (s *Service) ResumeGraphRun(ctx context.Context, p core.Principal, graphRunID string, step bool) error {
	rec, err := s.Jobs.Get(ctx, graphRunID)
	if err != nil {
		return err
	}
	if rec.Kind != core.JobKindGraph {
		return fmt.Errorf("%s is not a graph run", graphRunID)
	}
	if err := core.RequireTenant(p, rec.Tenant); err != nil {
		return err
	}
	if core.IsTerminalStatus(rec.Status) {
		return fmt.Errorf("run %s is %s: %w", graphRunID, rec.Status, core.ErrConflict)
	}

	var g core.Graph
	if len(rec.GraphPayload) > 0 {
		if err := json.Unmarshal(rec.GraphPayload, &g); err != nil {
			return fmt.Errorf("unmarshal graph: %w", err)
		}
	}
	if err := core.AuthorizeGraphRun(p, g); err != nil {
		return err
	}

	paused := breakpoints.takePaused(graphRunID)
	if len(paused) == 0 {
		return fmt.Errorf("run %s is not paused: %w", graphRunID, core.ErrConflict)
	}
	breakpoints.setStepping(graphRunID, step)

	// Detach from the request context: dispatch writes job records and must
	// not be cancelled when the HTTP handler returns (same as the worker).
	disp := NewDispatcher(s.Jobs, s.bus(), s.Engine, log.New(log.Writer(), "resume: ", log.LstdFlags))
	disp.resumeFrom(context.WithoutCancel(ctx), g, graphRunID, paused)
	return nil
}

// ResumeFailedRun retries a failed (or cancelled) run by re-executing only
// the part that didn't succeed — the failed node and everything downstream
// of it — while reusing the outputs of the nodes that already succeeded.
// Returns the new run's ID.
//
// Mechanism: it reuses the exact seed machinery webhook triggers use
// (SubmitGraphWithSeed / populateSeededRun). Every previously-succeeded
// node is pre-completed in the new run as a seed carrying its old output;
// the seed path then enqueues the frontier — the first not-yet-satisfied
// node, i.e. the one that failed — and normal worker dispatch drives the
// rest. No engine or dispatcher change is needed.
//
// Caveat handled here: a failed graph run reclaims its scratch space (see
// dispatch.go markGraphFailed → reclaimScratch), so any node output stored
// as a scratch Ref is gone. We therefore only seed nodes whose outputs are
// fully inline (self-contained in the DB record). A succeeded node with a
// scratch-backed output is deliberately NOT seeded, so it re-runs — and
// because not seeding a node makes the seed/dispatch chain re-enqueue it
// and its descendants, the partial recomputation stays correct.
func (s *Service) ResumeFailedRun(ctx context.Context, p core.Principal, runID string) (string, error) {
	rec, err := s.GetJob(ctx, p, runID)
	if err != nil {
		return "", err
	}
	if rec.Kind != core.JobKindGraph {
		return "", fmt.Errorf("%w: %q is a node record, not a run", core.ErrNotFound, runID)
	}
	switch rec.Status {
	case core.JobStatusFailed, core.JobStatusCancelled:
		// retryable — terminal but incomplete
	default:
		return "", fmt.Errorf("%w: run is %s; only failed or cancelled runs can be retried",
			core.ErrConflict, rec.Status)
	}
	if len(rec.GraphPayload) == 0 {
		return "", fmt.Errorf("run %q has no stored graph to retry", runID)
	}
	var g core.Graph
	if err := json.Unmarshal(rec.GraphPayload, &g); err != nil {
		return "", fmt.Errorf("decode stored graph for run %q: %w", runID, err)
	}

	nodes, err := s.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant:     rec.Tenant,
		Workspace:  rec.Workspace,
		GraphRunID: runID,
		Limit:      5000,
	})
	if err != nil {
		return "", fmt.Errorf("list node records for run %q: %w", runID, err)
	}

	// Seed every node that succeeded with reusable (inline) output. Nodes
	// that failed, were skipped, never ran, or whose output was reclaimed
	// are left out so they re-execute.
	seeds := make(map[string]core.Result, len(nodes))
	for _, n := range nodes {
		if n.Status != core.JobStatusSucceeded {
			continue
		}
		if !outputsReusable(n.Result) {
			continue
		}
		if n.Result != nil {
			seeds[n.NodeID] = *n.Result
		} else {
			seeds[n.NodeID] = core.Result{Status: core.StatusOK}
		}
	}

	// The stored graph carries the original tenant/workspace/id; the seed
	// path validates seed targets against it and runs under p's authz.
	//
	// Manual, because every way to reach a retry is a person pressing a button
	// in front of the failure they are retrying — the editor's error banner, the
	// runs list, the run-detail page. The original run already emailed about
	// this failure if it was going to; a retry that fails should not send a
	// second mail about the thing they are actively working on.
	return s.SubmitGraphOpts(ctx, p, g, SubmitOpts{Seeds: seeds, Manual: true})
}

// outputsReusable reports whether a succeeded node's outputs can be reused
// verbatim in a resumed run. Scratch-backed outputs (Ref set) are gone
// after a failed run reclaims its scratch, so a node carrying any such
// output is not reusable and must re-run. Inline outputs live in the DB
// record and are always safe. A nil result (succeeded with no output) is
// trivially reusable.
func outputsReusable(res *core.Result) bool {
	if res == nil {
		return true
	}
	for _, ref := range res.Output {
		if ref.Ref != "" {
			return false
		}
	}
	return true
}
