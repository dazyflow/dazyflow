// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

// recordSkippedFire writes a terminal "skipped" graph run so a cap-blocked
// scheduled fire shows up in the Runs list.
func TestRecordSkippedFire(t *testing.T) {
	jobs := jobstore.NewMemory()
	svc := &Service{Jobs: jobs}

	svc.recordSkippedFire(t.Context(), "t", "ws", "daily")

	recs, err := jobs.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
		Tenant: "t", Status: core.JobStatusSkipped, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListGraphRuns: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d skipped runs, want 1", len(recs))
	}
	if recs[0].GraphID != "daily" || recs[0].Workspace != "ws" {
		t.Errorf("marker = %+v, want graph=daily ws=ws", recs[0])
	}
	// No Jobs store → no-op, no panic.
	(&Service{}).recordSkippedFire(t.Context(), "t", "ws", "daily")
}

// The Runs-list marker is coalesced to one per flow per window so a
// frequent cron at the cap doesn't flood the list; the precise count lives
// in the usage counter instead.
func TestSchedulerSkipMarkerCoalesces(t *testing.T) {
	sched := NewScheduler(&Service{})
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sched.SetClock(func() time.Time { return now })

	if !sched.markSkip("t", "ws", "g") {
		t.Fatal("first mark should write")
	}
	if sched.markSkip("t", "ws", "g") {
		t.Fatal("second mark within window should coalesce to false")
	}
	if !sched.markSkip("t", "ws", "other") {
		t.Fatal("a different flow marks independently")
	}
	now = now.Add(skipMarkerWindow + time.Minute)
	if !sched.markSkip("t", "ws", "g") {
		t.Fatal("after the window elapses, marks again")
	}
}
