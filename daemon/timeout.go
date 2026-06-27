// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// effectiveGraphTimeout picks the timeout that applies to a run:
// per-graph if set, otherwise the operator ceiling, otherwise zero
// (no cap). Caller checks for zero before starting a watchdog so we
// don't spawn idle goroutines.
func (s *Service) effectiveGraphTimeout(g core.Graph) time.Duration {
	var d time.Duration
	if g.TimeoutSeconds > 0 {
		d = time.Duration(g.TimeoutSeconds) * time.Second
	}
	// Hard ceiling: clamp even an explicit per-graph value so a tenant
	// can't pin a worker for an unbounded duration. When the graph
	// itself sets no timeout, the ceiling becomes the de-facto default.
	// The ceiling is the tenant's effective limit (tier/override), which
	// falls back to the global MaxGraphTimeoutSeconds. effectiveLimits
	// reads the in-memory entitlement cache, so a background context is fine.
	ceilingSecs := s.effectiveLimits(context.Background(), g.Tenant).MaxTimeoutSeconds
	if max := time.Duration(ceilingSecs) * time.Second; max > 0 && (d == 0 || d > max) {
		d = max
	}
	return d
}

// startGraphTimeoutWatchdog launches a goroutine that auto-cancels
// runID after timeout if the run hasn't reached a terminal state by
// then. Returns immediately; the goroutine exits early when it sees a
// Terminal bus event, so a fast-completing run doesn't keep a timer
// alive for nothing.
//
// Watchdogs do NOT survive an dzd restart — a deployment that needs
// crash-safe enforcement should also wire a periodic sweep at
// startup (out of scope for v1; flagged in the TODO).
func (s *Service) startGraphTimeoutWatchdog(runID, tenant, workspace string, timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	go s.runGraphTimeoutWatchdog(runID, tenant, workspace, timeout)
}

func (s *Service) runGraphTimeoutWatchdog(runID, tenant, workspace string, timeout time.Duration) {
	events, cancelSub := s.bus().Subscribe(runID)
	defer cancelSub()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Terminal != nil {
				// Run finished on its own — nothing for us to do.
				return
			}
		case <-timer.C:
			// Mint a system principal with the same shape the
			// scheduler uses: PermGraphRun lets us cancel; PermGraphAdmin
			// lets us bypass private-flow visibility if the run was on
			// a private flow whose owner isn't us.
			sysP := SystemPrincipal("dazyflow-timeout", tenant, workspace)
			ctx, cancelCtx := context.WithTimeout(context.Background(), 30*time.Second)
			err := s.CancelGraphRun(ctx, sysP, runID, fmt.Sprintf("graph timeout after %s", timeout))
			cancelCtx()
			// ErrConflict means the run finished between the timer
			// firing and our Get — totally fine, ignore. Any other
			// error is worth logging because something is wrong with
			// the cancel path itself.
			if err != nil && !errors.Is(err, core.ErrConflict) {
				log.Printf("dazyflow-timeout: cancel %s: %v", runID, err)
			}
			return
		}
	}
}
