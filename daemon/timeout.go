// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// maxDurationSeconds is the largest second count that fits in an int64-ns
// time.Duration without overflow.
const maxDurationSeconds = int64(math.MaxInt64 / int64(time.Second))

// secondsToDuration converts a (possibly hostile) seconds count to a Duration,
// guarding the overflow that wraps a huge value to a NEGATIVE duration. A
// negative duration is neither 0 nor > the ceiling, so it slips past the ">0"
// timeout guards and silently disables the very watchdog/deadline it was meant
// to set. Clamp instead: <=0 → 0 (no timeout from this value), and an
// over-large value → the max representable so the ceiling clamp still applies.
func secondsToDuration(secs int) time.Duration {
	if secs <= 0 {
		return 0
	}
	if int64(secs) > maxDurationSeconds {
		return time.Duration(maxDurationSeconds) * time.Second
	}
	return time.Duration(secs) * time.Second
}

// effectiveGraphTimeout picks the timeout that applies to a run:
// per-graph if set, otherwise the operator ceiling, otherwise zero
// (no cap). Caller checks for zero before starting a watchdog so we
// don't spawn idle goroutines.
func (s *Service) effectiveGraphTimeout(g core.Graph) time.Duration {
	d := secondsToDuration(g.TimeoutSeconds)
	// Hard ceiling: clamp even an explicit per-graph value so a tenant
	// can't pin a worker for an unbounded duration. When the graph
	// itself sets no timeout, the ceiling becomes the de-facto default.
	// The ceiling is the tenant's effective limit (tier/override), which
	// falls back to the global MaxGraphTimeoutSeconds. effectiveLimits
	// reads the in-memory entitlement cache, so a background context is fine.
	ceilingSecs := s.effectiveLimits(context.Background(), g.Tenant).MaxTimeoutSeconds
	// Route the ceiling through secondsToDuration too: a misconfigured huge
	// operator/tier value would otherwise overflow negative and silently
	// disable the ceiling rather than capping the run.
	if max := secondsToDuration(ceilingSecs); max > 0 && (d == 0 || d > max) {
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
// Watchdogs do NOT survive a dzd restart — a deployment that needs
// crash-safe enforcement should also wire a periodic sweep at startup.
// Out of scope for v1, and deliberately not in TODO.md: nothing has asked
// for it, and the orphaned-graph-run reaper (DAZYFLOW_REAP_INTERVAL)
// already closes the runs a crash strands, just without honouring their
// per-graph timeout.
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
