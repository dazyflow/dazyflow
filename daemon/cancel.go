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

// CancelGraphRun aborts an in-flight graph run gracefully. The graph-record is
// marked Cancelled, then every non-terminal node-record, and a Terminal event is
// published so SSE subscribers wrap up.
//
// "Graceful" means nodes mid-execution are NOT interrupted — they finish
// naturally and the worker's AdvanceAfterCompletion is short-circuited once the
// graph-record is terminal. That keeps the cancel path safe for nodes that don't
// cooperate with context cancellation (external HTTP calls, sleeps, sandbox
// processes) while still guaranteeing no further downstream work starts.
//
// Errors:
//   - core.ErrNotFound if the run doesn't exist
//   - core.ErrConflict if the run is already terminal
//   - core.ErrUnauthorized when the principal lacks graph:run on the graph

// CancelCodeByPerson marks a cancel somebody asked for, as opposed to one the
// platform imposed (CancelCodeTimeout). The failure-notification sweep reads it
// to decide whether a cancelled run is worth an email: stopping your own run
// needs no telling, but a run the platform stopped does — and it reads as
// "cancelled" in the Runs list, which looks like somebody meant it.
const (
	CancelCodeByPerson = "cancelled"
	CancelCodeTimeout  = "timeout"
)

func (s *Service) CancelGraphRun(ctx context.Context, p core.Principal, graphRunID, reason string) error {
	return s.cancelGraphRun(ctx, p, graphRunID, CancelCodeByPerson, reason)
}

func (s *Service) cancelGraphRun(ctx context.Context, p core.Principal, graphRunID, code, reason string) error {
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

	if reason == "" {
		reason = fmt.Sprintf("cancelled by %s", p.Subject)
	}
	cancelErr := &core.JobError{Code: code, Message: reason}
	// cancelledResult stamps the cancel onto a node while keeping whatever it had
	// already published. For a parked await_approval node that result is the only
	// record of what it was waiting on (prompt, value, approval URL), and the run
	// page and approvals history need it. Mirrors Approve. Preserved output cannot
	// leak downstream: classifyEdge blocks every edge out of a cancelled record.
	cancelledResult := func(prev *core.Result) *core.Result {
		res := &core.Result{Status: core.StatusError, Error: cancelErr}
		if prev == nil || len(prev.Output) == 0 {
			return res
		}
		res.Output = make(map[string]core.Ref, len(prev.Output))
		for port, ref := range prev.Output {
			res.Output[port] = ref
		}
		return res
	}

	// Flip the graph-record FIRST, then sweep. Workers race against us, and the
	// graph-record is what they check: once it is terminal CompleteAndEnqueue
	// queues no dependents and a worker that claims a node of this run cancels it
	// instead of executing it. Sweeping first left a window where a node finishing
	// mid-sweep queued a dependent the sweep had already walked past, and that
	// step then ran after the cancel.
	breakpoints.clear(graphRunID)
	graphResult := &core.Result{Status: core.StatusError, Error: cancelErr}
	if err := s.Jobs.Complete(ctx, graphRunID, core.JobStatusCancelled, graphResult); err != nil {
		if errors.Is(err, core.ErrConflict) {
			return fmt.Errorf("run %s already finished: %w", graphRunID, core.ErrConflict)
		}
		return fmt.Errorf("cancel graph record: %w", err)
	}
	for _, n := range g.Nodes {
		nodeRecID := NodeJobID(graphRunID, n.ID)
		nrec, err := s.Jobs.Get(ctx, nodeRecID)
		if err != nil {
			continue
		}
		if core.IsTerminalStatus(nrec.Status) {
			continue
		}
		// Complete is idempotent: if the worker beat us with Succeeded/
		// Failed we'll get ErrConflict here, which is fine — that node
		// already advanced and the graph-status guard will keep its
		// dispatch attempt from doing damage.
		if err := s.Jobs.Complete(ctx, nodeRecID, core.JobStatusCancelled, cancelledResult(nrec.Result)); err == nil {
			s.bus().Publish(graphRunID, BusEvent{NodeStatus: &NodeStatusEvent{
				NodeID: n.ID,
				Status: core.JobStatusCancelled,
				Error:  cancelErr,
			}})
		} else if !errors.Is(err, core.ErrConflict) {
			// The run is already cancelled; a node left behind here is cancelled by
			// the worker that claims it, so keep sweeping rather than stop half-way.
			s.logf("cancel %s: node %s: %v", graphRunID, n.ID, err)
		}
		// A subgraph node parks on its child run; the child is this run's work too
		// and must stop with it (a cancel or a timeout alike).
		if nrec.Status == core.JobStatusAwaiting && nrec.Result != nil {
			if childGraphID, _ := nrec.Result.Output["pending_child_graph_id"].Inline.(string); childGraphID != "" {
				s.cancelChildRuns(ctx, rec.Tenant, rec.Workspace, childGraphID, nodeRecID, reason)
			}
		}
	}

	disp := NewDispatcher(s.Jobs, s.bus(), s.Engine, s.Logger)
	disp.reclaimScratch(g, graphRunID)
	s.bus().Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
		JobID:  graphRunID,
		Status: core.JobStatusCancelled,
		Error:  cancelErr,
	}})
	// A child run cancelled on its own must not leave its parent's subgraph node
	// parked for ever: fail that node so the parent run moves on. A no-op when the
	// parent is what cancelled us — its node is already cancelled.
	if rec.ParentNodeRecID != "" {
		disp.maybeResumeParent(ctx, graphRunID, core.StatusError, cancelErr)
	}
	return nil
}

// CancelCodeParent marks a child run stopped because its parent run was
// cancelled or timed out. The parent's own outcome is what gets reported, so
// the failure-notification sweep does not mail about the child as well.
const CancelCodeParent = "parent_cancelled"

func (s *Service) cancelChildRuns(ctx context.Context, tenant, workspace, childGraphID, parentNodeRecID, reason string) {
	p := SystemPrincipal("dazyflow-cancel", tenant, workspace)
	for _, st := range []core.JobStatus{core.JobStatusRunning, core.JobStatusAwaiting, core.JobStatusQueued} {
		runs, err := listGraphRunsOldest(ctx, s.Jobs, core.ListGraphRunsOpts{
			Tenant: tenant, Workspace: workspace, GraphID: childGraphID, Status: st, Limit: 500,
		})
		if err != nil {
			s.logf("cancel children of %s: list %s runs: %v", parentNodeRecID, st, err)
			continue
		}
		for _, r := range runs {
			if r.ParentNodeRecID != parentNodeRecID {
				continue
			}
			if err := s.cancelGraphRun(ctx, p, r.ID, CancelCodeParent, "parent run stopped: "+reason); err != nil &&
				!errors.Is(err, core.ErrConflict) {
				s.logf("cancel child run %s of %s: %v", r.ID, parentNodeRecID, err)
			}
		}
	}
}
