// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// oldestStore is the sweep-side surface both stores implement in *_oldest.go.
type oldestStore interface {
	core.JobStore
	ListGraphRunsOldest(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.JobRecord, error)
	ListGraphRunSummariesOldest(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.RunSummary, error)
	GraphRunTenants(ctx context.Context, status core.JobStatus) ([]string, error)
	MarkGraphRunningCapped(ctx context.Context, jobID, tenant string, limit int) (started, atCap bool, err error)
}

func runOldestConformance(t *testing.T, mk func(t *testing.T) oldestStore) {
	seed := func(t *testing.T, s oldestStore) {
		now := time.Now()
		add := func(id, tenant string, status core.JobStatus, age time.Duration) {
			mustEnqueue(t, s, t.Context(), core.JobRecord{
				ID: id, Kind: core.JobKindGraph, Status: status,
				Tenant: tenant, Workspace: "ws", GraphID: "g",
				EnqueuedAt: now.Add(-age),
			})
		}
		add("q-new", "acme", core.JobStatusQueued, time.Minute)
		add("q-old", "acme", core.JobStatusQueued, time.Hour)
		add("q-mid", "acme", core.JobStatusQueued, 10*time.Minute)
		add("q-globex", "globex", core.JobStatusQueued, 2*time.Hour)
		add("r-acme", "acme", core.JobStatusRunning, 3*time.Hour)
		mustEnqueue(t, s, t.Context(), core.JobRecord{
			ID: "n1", Kind: core.JobKindNode, Tenant: "initech", Status: core.JobStatusQueued,
			EnqueuedAt: now.Add(-5 * time.Hour),
		})
	}

	t.Run("ListGraphRunsOldest_orders_ascending", func(t *testing.T) {
		s := mk(t)
		seed(t, s)
		got, err := s.ListGraphRunsOldest(t.Context(), core.ListGraphRunsOpts{
			Tenant: "acme", Status: core.JobStatusQueued, Limit: 2,
		})
		if err != nil {
			t.Fatalf("ListGraphRunsOldest: %v", err)
		}
		if want := []string{"q-old", "q-mid"}; !slices.Equal(ids(got), want) {
			t.Errorf("got %v, want %v", ids(got), want)
		}
		got, err = s.ListGraphRunsOldest(t.Context(), core.ListGraphRunsOpts{
			Tenant: "acme", Status: core.JobStatusQueued, Limit: 2, Offset: 2,
		})
		if err != nil {
			t.Fatalf("ListGraphRunsOldest page 2: %v", err)
		}
		if want := []string{"q-new"}; !slices.Equal(ids(got), want) {
			t.Errorf("page 2: got %v, want %v", ids(got), want)
		}
	})

	t.Run("ListGraphRunSummariesOldest_orders_ascending", func(t *testing.T) {
		s := mk(t)
		seed(t, s)
		got, err := s.ListGraphRunSummariesOldest(t.Context(), core.ListGraphRunsOpts{Status: core.JobStatusQueued})
		if err != nil {
			t.Fatalf("ListGraphRunSummariesOldest: %v", err)
		}
		var gotIDs []string
		for _, r := range got {
			gotIDs = append(gotIDs, r.ID)
		}
		if want := []string{"q-globex", "q-old", "q-mid", "q-new"}; !slices.Equal(gotIDs, want) {
			t.Errorf("got %v, want %v", gotIDs, want)
		}
	})

	t.Run("GraphRunTenants_lists_graph_tenants_only", func(t *testing.T) {
		s := mk(t)
		seed(t, s)
		got, err := s.GraphRunTenants(t.Context(), core.JobStatusQueued)
		if err != nil {
			t.Fatalf("GraphRunTenants: %v", err)
		}
		slices.Sort(got)
		if want := []string{"acme", "globex"}; !slices.Equal(got, want) {
			t.Errorf("got %v, want %v (node-kind tenants must not appear)", got, want)
		}
	})

	t.Run("MarkGraphRunningCapped_holds_the_cap", func(t *testing.T) {
		s := mk(t)
		seed(t, s) // acme already has one running
		started, atCap, err := s.MarkGraphRunningCapped(t.Context(), "q-old", "acme", 1)
		if err != nil || started || !atCap {
			t.Fatalf("at cap: started=%v atCap=%v err=%v, want false,true,nil", started, atCap, err)
		}
		started, atCap, err = s.MarkGraphRunningCapped(t.Context(), "q-old", "acme", 2)
		if err != nil || !started || atCap {
			t.Fatalf("below cap: started=%v atCap=%v err=%v, want true,false,nil", started, atCap, err)
		}
		rec, err := s.Get(t.Context(), "q-old")
		if err != nil || rec.Status != core.JobStatusRunning {
			t.Fatalf("q-old = %v, %v; want running", rec.Status, err)
		}
		// Already running: not started again, and not reported as at cap.
		started, atCap, err = s.MarkGraphRunningCapped(t.Context(), "q-old", "acme", 5)
		if err != nil || started || atCap {
			t.Errorf("already running: started=%v atCap=%v err=%v, want false,false,nil", started, atCap, err)
		}
	})
}

func TestMemory_OldestConformance(t *testing.T) {
	runOldestConformance(t, func(*testing.T) oldestStore { return NewMemory() })
}

func TestPostgres_OldestConformance(t *testing.T) {
	url := testDB(t)
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(store.Close)
	runOldestConformance(t, func(t *testing.T) oldestStore {
		if _, err := store.pool.Exec(ctx, "TRUNCATE jobs"); err != nil {
			t.Fatalf("TRUNCATE: %v", err)
		}
		return store
	})
}
