// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"log"

	"github.com/dazyflow/dazyflow/core"
)

// Run-level "waiting for approval" status.
//
// A node that parks on an approval gets status=awaiting on its own record, but
// the RUN kept reporting Running — so the runs list showed a flow that had
// been sitting on a person for two days as though it were busy working, and
// the list's "Waiting" filter matched nothing. The run status is the one most
// people look at; this makes it tell the truth.
//
// Only an approval pause counts. A subgraph node also parks as awaiting while
// its child graph runs, and that run is not waiting on anybody — it has work
// in flight. isApprovalPause draws the same line the Approvals inbox and the
// mail hook draw: the pause emitted a pending_url, so there is something for a
// human to decide.
//
// Everything here is best-effort. The run status is a display concern; failing
// to update it must never fail the park that already committed, nor the
// decision that resumed it.

// isApprovalPause reports whether a parked Result is one a person has to
// resolve, as opposed to a subgraph waiting on its child.
func isApprovalPause(result *core.Result) bool {
	if result == nil || result.Output == nil {
		return false
	}
	_, ok := result.Output["pending_url"]
	return ok
}

// setRunParked flips the graph record between running and awaiting. Stores
// that don't implement core.GraphRunParker simply keep the old behaviour
// (the run stays Running), which is why the type assertion is not an error.
func setRunParked(ctx context.Context, store core.JobStore, logger *log.Logger, graphRunID string, parked bool) {
	parker, ok := store.(core.GraphRunParker)
	if !ok {
		return
	}
	if _, err := parker.SetGraphRunParked(ctx, graphRunID, parked); err != nil && logger != nil {
		logger.Printf("run %s: mark parked=%v: %v", graphRunID, parked, err)
	}
}

// runHasParkedApproval reports whether the run still has any node parked on a
// human decision. Called before un-parking the run on a resume: a flow can
// hold two approvals open at once (two branches, two approvers), and the first
// decision must not flip the run back to Running while the second still waits.
//
// Errs on the side of "still parked" when the store read fails: a run wrongly
// showing Waiting corrects itself on the next resume, whereas wrongly showing
// Running hides a flow that needs someone.
func runHasParkedApproval(ctx context.Context, store core.JobStore, graphRunID, excludeNodeID string) bool {
	recs, err := store.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		GraphRunID: graphRunID,
		Status:     core.JobStatusAwaiting,
		Limit:      200,
	})
	if err != nil {
		return true
	}
	for _, rec := range recs {
		if rec.NodeID == excludeNodeID {
			continue
		}
		if isApprovalPause(rec.Result) {
			return true
		}
	}
	return false
}
