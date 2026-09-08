// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

func TestRecordSkippedFire(t *testing.T) {
	t.Parallel()
	jobs := jobstore.NewMemory()
	svc := &Service{Jobs: jobs}

	svc.recordSkippedFire(t.Context(), "t", "ws", "daily", "plan_run_cap", "over the limit")

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
	(&Service{}).recordSkippedFire(t.Context(), "t", "ws", "daily", "plan_run_cap", "x")
}

func TestSchedulerSkipMarkerCoalesces(t *testing.T) {
	t.Parallel()
	sched := NewScheduler(&Service{})
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sched.SetClock(func() time.Time { return now })

	if !sched.markOnce("cap", "t", "ws", "g") {
		t.Fatal("first mark should write")
	}
	if sched.markOnce("cap", "t", "ws", "g") {
		t.Fatal("second mark within window should coalesce to false")
	}
	if !sched.markOnce("cap", "t", "ws", "other") {
		t.Fatal("a different flow marks independently")
	}
	if !sched.markOnce("published_flow_unreadable", "t", "ws", "g") {
		t.Fatal("a different problem with the same flow should mark independently")
	}
	now = now.Add(skipMarkerWindow + time.Minute)
	if !sched.markOnce("cap", "t", "ws", "g") {
		t.Fatal("after the window elapses, marks again")
	}
}
