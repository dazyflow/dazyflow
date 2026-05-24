package jobstore

import (
	"errors"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
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
