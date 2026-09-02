// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"sync"

	"github.com/dazyflow/dazyflow/core"
)

// pauseRegistry tracks breakpoint pause state per graph run (#12).
//
// It is in-memory and process-local. That is sufficient for a single-node
// daemon: a paused run simply idles until Continue/Step. If the process
// restarts while a run is paused, the pause state is lost and the run idles
// (eventually reaped by the graph timeout watchdog, if any). A multi-node
// or crash-durable implementation would persist this alongside the run.
type pauseRegistry struct {
	mu sync.Mutex
	// paused maps a graph-run ID to the node IDs the run is currently
	// paused *after* — their dependents were held rather than dispatched.
	paused map[string][]string
	// stepping maps a graph-run ID to step mode: while on, the run pauses
	// after every node (not just breakpoint nodes), until Continue clears it.
	stepping map[string]bool
}

// breakpoints is the process-wide registry. Dispatcher and Service are
// constructed in several places (worker, approval/cancel/resume), so the
// shared pause state lives at package scope rather than on either struct.
var breakpoints = &pauseRegistry{
	paused:   map[string][]string{},
	stepping: map[string]bool{},
}

// addPaused records that runID paused after nodeID (dedup).
func (r *pauseRegistry) addPaused(runID, nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.paused[runID] {
		if id == nodeID {
			return
		}
	}
	r.paused[runID] = append(r.paused[runID], nodeID)
}

// takePaused returns and clears the nodes runID is paused after.
func (r *pauseRegistry) takePaused(runID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.paused[runID]
	delete(r.paused, runID)
	return ids
}

func (r *pauseRegistry) setStepping(runID string, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if on {
		r.stepping[runID] = true
	} else {
		delete(r.stepping, runID)
	}
}

func (r *pauseRegistry) isStepping(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stepping[runID]
}

// clear drops all pause state for a run (called when it terminates).
func (r *pauseRegistry) clear(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.paused, runID)
	delete(r.stepping, runID)
}

// shouldPauseAfter reports whether a just-succeeded node should hold the run:
// either the run is in step mode, or the node carries a breakpoint AND somebody
// is watching this run.
//
// watched is what keeps a breakpoint a debugging aid. It lives in the saved
// graph, so a published flow carries it into every run a trigger starts — and
// nothing finalizes or reaps a paused run (the reaper reads its un-dispatched
// dependents as outstanding work), so each unattended fire used to leave a
// non-terminal run behind for good, taking a concurrency slot with it. Step
// mode is unconditional: it is set per run through the Step API, which only a
// watching person can call.
func shouldPauseAfter(graph core.Graph, runID, nodeID string, watched bool) bool {
	if breakpoints.isStepping(runID) {
		return true
	}
	if !watched {
		return false
	}
	if n, ok := graph.Node(nodeID); ok {
		return n.Breakpoint
	}
	return false
}
