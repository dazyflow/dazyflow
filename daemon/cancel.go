package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// CancelGraphRun aborts an in-flight graph run gracefully. Every
// non-terminal node-record is marked Cancelled, the graph-record is
// marked Cancelled, and a Terminal event is published so SSE
// subscribers wrap up.
//
// "Graceful" means we do NOT interrupt nodes that are mid-execution —
// they finish naturally and the worker's call to
// Dispatcher.AdvanceAfterCompletion is short-circuited once the
// graph-record is terminal (see dispatch.go). This keeps the cancel
// path safe for nodes that don't cooperate with context cancellation
// (external HTTP calls, sleeps, sandbox processes) while still
// guaranteeing that no further downstream work starts.
//
// Errors:
//   - core.ErrNotFound if the run doesn't exist
//   - core.ErrConflict if the run is already in a terminal state
//   - core.ErrUnauthorized when the principal lacks graph:run on the
//     stored graph
func (s *Service) CancelGraphRun(ctx context.Context, p core.Principal, graphRunID, reason string) error {
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
	// Authorize against the stored graph payload: visibility may have
	// changed since the run started, but the user who can re-run it
	// should also be able to cancel it.
	if err := core.AuthorizeGraphRun(p, g); err != nil {
		return err
	}

	if reason == "" {
		reason = fmt.Sprintf("cancelled by %s", p.Subject)
	}
	cancelErr := &core.JobError{Code: "cancelled", Message: reason}
	nodeResult := &core.Result{Status: core.StatusError, Error: cancelErr}

	// Sweep non-terminal node-records first so the dispatcher's
	// short-circuit (introduced for cancel) is in place before we flip
	// the graph-record. Order matters: workers race against us, and
	// once the graph-record is terminal the dispatcher refuses to
	// advance — so a node racing to Complete after we mark the graph
	// cannot cause spurious dispatch.
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
		if err := s.Jobs.Complete(ctx, nodeRecID, core.JobStatusCancelled, nodeResult); err == nil {
			s.bus().Publish(graphRunID, BusEvent{NodeStatus: &NodeStatusEvent{
				NodeID: n.ID,
				Status: core.JobStatusCancelled,
				Error:  cancelErr,
			}})
		} else if !errors.Is(err, core.ErrConflict) {
			return fmt.Errorf("cancel node %s: %w", n.ID, err)
		}
	}

	graphResult := &core.Result{Status: core.StatusError, Error: cancelErr}
	if err := s.Jobs.Complete(ctx, graphRunID, core.JobStatusCancelled, graphResult); err != nil {
		return fmt.Errorf("cancel graph record: %w", err)
	}
	s.bus().Publish(graphRunID, BusEvent{Terminal: &TerminalEvent{
		JobID:  graphRunID,
		Status: core.JobStatusCancelled,
		Error:  cancelErr,
	}})
	return nil
}
