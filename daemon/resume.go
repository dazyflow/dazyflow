package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"git.sr.ht/~klahr/hazyflow/core"
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
