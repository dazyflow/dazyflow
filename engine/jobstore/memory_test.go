// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func TestMemory_EnqueueAndClaim(t *testing.T) {
	s := NewMemory()
	job := core.JobRecord{ID: "j1", Kind: core.JobKindNode, GraphID: "g", NodeID: "n", Tenant: "t"}
	if err := s.Enqueue(t.Context(), job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got, err := s.Claim(t.Context(), "worker-1", 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got.Status != core.JobStatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.WorkerID != "worker-1" {
		t.Errorf("worker = %q", got.WorkerID)
	}
	if got.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", got.Attempt)
	}
}

func TestMemory_ClaimReturnsErrNoJobs(t *testing.T) {
	s := NewMemory()
	_, err := s.Claim(t.Context(), "w", time.Second)
	if !errors.Is(err, core.ErrNoJobs) {
		t.Errorf("err = %v, want ErrNoJobs", err)
	}
}

func TestMemory_LeaseExpiryReclaim(t *testing.T) {
	s := NewMemory()
	now := time.Unix(0, 0)
	s.clock = func() time.Time { return now }

	_ = s.Enqueue(t.Context(), core.JobRecord{ID: "j1", Kind: core.JobKindNode})
	if _, err := s.Claim(t.Context(), "w1", time.Minute); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// Advance past the lease.
	now = now.Add(2 * time.Minute)
	got, err := s.Claim(t.Context(), "w2", time.Minute)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if got.WorkerID != "w2" {
		t.Errorf("worker = %q, want w2", got.WorkerID)
	}
	if got.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", got.Attempt)
	}
}

func TestMemory_RenewAndComplete(t *testing.T) {
	s := NewMemory()
	_ = s.Enqueue(t.Context(), core.JobRecord{ID: "j1", Kind: core.JobKindNode})
	_, _ = s.Claim(t.Context(), "w", time.Minute)

	if err := s.Renew(t.Context(), "j1", "w", time.Minute); err != nil {
		t.Errorf("Renew: %v", err)
	}
	if err := s.Renew(t.Context(), "j1", "different-worker", time.Minute); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Renew wrong worker: err = %v, want ErrConflict", err)
	}
	if err := s.Complete(t.Context(), "j1", core.JobStatusSucceeded, &core.Result{Status: "ok"}); err != nil {
		t.Errorf("Complete: %v", err)
	}
	got, _ := s.Get(t.Context(), "j1")
	if got.Status != core.JobStatusSucceeded {
		t.Errorf("status = %q, want succeeded", got.Status)
	}
	if got.LeaseUntil != nil {
		t.Errorf("lease should be cleared after complete")
	}
}

func TestMemory_ListByGraph(t *testing.T) {
	s := NewMemory()
	for _, id := range []string{"a", "b", "c"} {
		_ = s.Enqueue(t.Context(), core.JobRecord{ID: id, Kind: core.JobKindNode, GraphID: "g1"})
	}
	_ = s.Enqueue(t.Context(), core.JobRecord{ID: "z", Kind: core.JobKindNode, GraphID: "other"})

	got, _ := s.ListByGraph(t.Context(), "g1")
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
	for _, r := range got {
		if r.GraphID != "g1" {
			t.Errorf("graph = %q, want g1", r.GraphID)
		}
	}
}

func TestMemory_MaxConcurrentPerTenant(t *testing.T) {
	s := NewMemory()
	s.SetMaxConcurrentPerTenant(2)
	for _, id := range []string{"a1", "a2", "a3"} {
		if err := s.Enqueue(t.Context(), core.JobRecord{ID: id, Kind: core.JobKindNode, Tenant: "acme"}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}

	// Two claims succeed — acme is then at its cap of 2.
	c1, err := s.Claim(t.Context(), "w", 30*time.Second)
	if err != nil {
		t.Fatalf("claim 1: %v", err)
	}
	if _, err := s.Claim(t.Context(), "w", 30*time.Second); err != nil {
		t.Fatalf("claim 2: %v", err)
	}
	// Third acme job is withheld: the tenant has 2 running.
	if _, err := s.Claim(t.Context(), "w", 30*time.Second); !errors.Is(err, core.ErrNoJobs) {
		t.Fatalf("claim 3 err = %v, want ErrNoJobs (acme at cap)", err)
	}

	// A different tenant is unaffected by acme's cap.
	if err := s.Enqueue(t.Context(), core.JobRecord{ID: "b1", Kind: core.JobKindNode, Tenant: "globex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(t.Context(), "w", 30*time.Second); err != nil {
		t.Fatalf("globex claim should succeed despite acme at cap: %v", err)
	}

	// Completing an acme job frees a slot, so the third becomes claimable.
	if err := s.Complete(t.Context(), c1.ID, core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatalf("complete %s: %v", c1.ID, err)
	}
	freed, err := s.Claim(t.Context(), "w", 30*time.Second)
	if err != nil {
		t.Fatalf("claim after freeing a slot: %v", err)
	}
	if freed.Tenant != "acme" {
		t.Errorf("freed claim tenant = %q, want acme", freed.Tenant)
	}
}

func TestMemory_MaxConcurrentExemptsExpiredReclaim(t *testing.T) {
	s := NewMemory()
	now := time.Unix(100, 0)
	s.clock = func() time.Time { return now }
	s.SetMaxConcurrentPerTenant(1)

	// Craft a tenant at its cap of 1 live-running job, plus a dead
	// (expired-lease) job and a fresh queued job. Injected directly: a
	// live AND an expired running job under a cap of 1 is unreachable
	// through Claim alone.
	future := time.Unix(200, 0)
	past := time.Unix(50, 0)
	s.records["live"] = &core.JobRecord{
		ID: "live", Kind: core.JobKindNode, Tenant: "acme",
		Status: core.JobStatusRunning, LeaseUntil: &future, EnqueuedAt: time.Unix(1, 0),
	}
	s.records["dead"] = &core.JobRecord{
		ID: "dead", Kind: core.JobKindNode, Tenant: "acme",
		Status: core.JobStatusRunning, LeaseUntil: &past, EnqueuedAt: time.Unix(2, 0),
	}
	s.records["queued"] = &core.JobRecord{
		ID: "queued", Kind: core.JobKindNode, Tenant: "acme",
		Status: core.JobStatusQueued, EnqueuedAt: time.Unix(3, 0),
	}

	// acme has 1 live-running job (== cap), so "queued" stays withheld —
	// but "dead" (expired lease) is recovery, exempt from the cap, so it
	// is what Claim hands back.
	got, err := s.Claim(t.Context(), "w", 10*time.Second)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if got.ID != "dead" {
		t.Errorf("claimed %q, want the expired job 'dead' (queued must stay withheld at cap)", got.ID)
	}
}

func TestMemory_CompleteOwned_FencesNonOwner(t *testing.T) {
	s := NewMemory()
	if err := s.Enqueue(t.Context(), core.JobRecord{ID: "j1", Kind: core.JobKindNode, Tenant: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(t.Context(), "worker-A", 30*time.Second); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// A worker that doesn't own the lease (e.g. lost it and was reclaimed)
	// must be fenced out — ErrConflict, record untouched.
	err := s.CompleteOwned(t.Context(), "j1", "worker-B", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK})
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("CompleteOwned by non-owner = %v, want ErrConflict", err)
	}
	if rec, _ := s.Get(t.Context(), "j1"); rec.Status != core.JobStatusRunning {
		t.Errorf("status = %q after fenced write, want still running", rec.Status)
	}

	// The actual owner completes fine.
	if err := s.CompleteOwned(t.Context(), "j1", "worker-A", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatalf("owner CompleteOwned: %v", err)
	}
	if rec, _ := s.Get(t.Context(), "j1"); rec.Status != core.JobStatusSucceeded {
		t.Errorf("status = %q, want succeeded", rec.Status)
	}
}

// Enqueue stamps StartedAt only for records that never pass through Claim
// — already-running graph records and already-terminal seeds. A queued
// record has not started, so stamping it would make every job report a
// run duration beginning at enqueue time.
func TestMemory_EnqueueDoesNotStampQueuedRecord(t *testing.T) {
	s := NewMemory()
	now := time.Unix(1_000, 0)
	s.clock = func() time.Time { return now }

	if err := s.Enqueue(t.Context(), core.JobRecord{
		ID: "j1", Kind: core.JobKindNode, Tenant: "t", Status: core.JobStatusQueued,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got, err := s.Get(t.Context(), "j1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.StartedAt != nil {
		t.Errorf("StartedAt = %v, want nil for a queued record", got.StartedAt)
	}
	if got.FinishedAt != nil {
		t.Errorf("FinishedAt = %v, want nil for a queued record", got.FinishedAt)
	}
}

// Enqueue fills in only what is missing: a record that arrives carrying
// its own StartedAt keeps it, so a replayed or restored run does not have
// its start time rewritten to "now".
func TestMemory_EnqueueStampsOnlyMissingStartedAt(t *testing.T) {
	s := NewMemory()
	now := time.Unix(1_000, 0)
	s.clock = func() time.Time { return now }
	started := time.Unix(500, 0)

	if err := s.Enqueue(t.Context(), core.JobRecord{
		ID: "g1", Kind: core.JobKindGraph, Tenant: "t",
		Status: core.JobStatusRunning, StartedAt: &started,
	}); err != nil {
		t.Fatalf("Enqueue g1: %v", err)
	}
	got, err := s.Get(t.Context(), "g1")
	if err != nil {
		t.Fatalf("Get g1: %v", err)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want the supplied %v", got.StartedAt, started)
	}

	// A running record with no StartedAt does get one stamped.
	if err := s.Enqueue(t.Context(), core.JobRecord{
		ID: "g2", Kind: core.JobKindGraph, Tenant: "t", Status: core.JobStatusRunning,
	}); err != nil {
		t.Fatalf("Enqueue g2: %v", err)
	}
	got2, err := s.Get(t.Context(), "g2")
	if err != nil {
		t.Fatalf("Get g2: %v", err)
	}
	if got2.StartedAt == nil || !got2.StartedAt.Equal(now) {
		t.Errorf("StartedAt = %v, want %v stamped at enqueue", got2.StartedAt, now)
	}
}

// With burst spacing switched off, a record's queue slot is its own
// enqueue time and nothing else, so claim order is strictly enqueue order
// however the records happened to arrive. Borrowing another record's slot
// when spacing is zero reorders the queue.
func TestMemory_ZeroBurstSpacingClaimsInEnqueueOrder(t *testing.T) {
	s := NewMemory()
	base := time.Unix(1_000, 0)
	s.clock = func() time.Time { return base }
	s.SetBurstSpacing(0)

	// Inserted out of order on purpose: j3 is the oldest despite arriving last.
	for _, tc := range []struct {
		id     string
		offset time.Duration
	}{
		{"j1", 10 * time.Second},
		{"j2", 20 * time.Second},
		{"j3", 5 * time.Second},
	} {
		if err := s.Enqueue(t.Context(), core.JobRecord{
			ID: tc.id, Kind: core.JobKindNode, Tenant: "t",
			EnqueuedAt: base.Add(tc.offset),
		}); err != nil {
			t.Fatalf("Enqueue %s: %v", tc.id, err)
		}
	}

	var order []string
	for range 3 {
		got, err := s.Claim(t.Context(), "w", time.Minute)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		order = append(order, got.ID)
	}
	if want := []string{"j3", "j1", "j2"}; !slices.Equal(order, want) {
		t.Errorf("claim order = %v, want %v (oldest enqueue first)", order, want)
	}
}

func TestMemory_ListGraphRunsFiltersByStatus(t *testing.T) {
	s := NewMemory()
	base := time.Unix(1_000, 0)
	for i, st := range []core.JobStatus{
		core.JobStatusRunning, core.JobStatusSucceeded, core.JobStatusRunning,
	} {
		if err := s.Enqueue(t.Context(), core.JobRecord{
			ID: fmt.Sprintf("g%d", i), Kind: core.JobKindGraph, Tenant: "t",
			Status: st, EnqueuedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("Enqueue g%d: %v", i, err)
		}
	}

	got, err := s.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
		Tenant: "t", Status: core.JobStatusRunning,
	})
	if err != nil {
		t.Fatalf("ListGraphRuns: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d runs, want 2 running", len(got))
	}
	// An inverted predicate returns the complement, which can have a
	// plausible size — so check the status of every row too.
	for _, r := range got {
		if r.Status != core.JobStatusRunning {
			t.Errorf("run %s has status %q, want running", r.ID, r.Status)
		}
	}
}

// An unset Limit means "count them all". It must not fall through to
// ListGraphRuns' default page of 50, and the ceiling it substitutes has to
// exceed the record count rather than land on it.
func TestMemory_CountGraphRunsCountsPastTheDefaultPage(t *testing.T) {
	s := NewMemory()
	base := time.Unix(1_000, 0)
	const n = 51 // deliberately above ListGraphRuns' default page of 50
	for i := range n {
		if err := s.Enqueue(t.Context(), core.JobRecord{
			ID: fmt.Sprintf("g%02d", i), Kind: core.JobKindGraph, Tenant: "t",
			EnqueuedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	got, err := s.CountGraphRuns(t.Context(), core.ListGraphRunsOpts{Tenant: "t"})
	if err != nil {
		t.Fatalf("CountGraphRuns: %v", err)
	}
	if got != n {
		t.Errorf("CountGraphRuns = %d, want %d", got, n)
	}
}

// Equal enqueue times are broken by DESCENDING id, matching the Postgres
// store's "enqueued_at DESC, id DESC". The direction is the point: it has
// to be a stable total order, so a tie on a page boundary can neither
// repeat nor drop a row across pages.
func TestMemory_ListNodeRecordsBreaksTiesByDescendingID(t *testing.T) {
	s := NewMemory()
	at := time.Unix(1_000, 0)
	for _, id := range []string{"a", "b", "c"} {
		if err := s.Enqueue(t.Context(), core.JobRecord{
			ID: id, Kind: core.JobKindNode, Tenant: "t", EnqueuedAt: at,
		}); err != nil {
			t.Fatalf("Enqueue %s: %v", id, err)
		}
	}

	got, err := s.ListNodeRecords(t.Context(), core.ListNodeRecordsOpts{Tenant: "t"})
	if err != nil {
		t.Fatalf("ListNodeRecords: %v", err)
	}
	ids := make([]string, 0, len(got))
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if want := []string{"c", "b", "a"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}
