// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// approvalFixture is the record set both store implementations are checked
// against: two settled approvals (one decided recently but enqueued long ago),
// one ordinary succeeded step, and one approval still parked.
func approvalFixture(t *testing.T, store core.JobStore, setFinished func(id string, at time.Time)) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	fin := func(d time.Duration) *time.Time { v := now.Add(d); return &v }

	recs := []core.JobRecord{{
		// Enqueued three weeks ago, decided a minute ago: the row that proves
		// the ordering column matters.
		ID: "old-decided", Kind: core.JobKindNode, GraphRunID: "run-old", NodeID: "gate",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusSucceeded,
		EnqueuedAt: now.Add(-21 * 24 * time.Hour), FinishedAt: fin(-time.Minute),
		Result: &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
			"pending_url": {Inline: "u"}, "approved": {Inline: "v"},
		}},
	}, {
		ID: "new-decided", Kind: core.JobKindNode, GraphRunID: "run-new", NodeID: "gate",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusSucceeded,
		EnqueuedAt: now.Add(-2 * time.Hour), FinishedAt: fin(-time.Hour),
		Result: &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
			"pending_url": {Inline: "u"}, "rejected": {Inline: "v"},
		}},
	}, {
		ID: "plain-step", Kind: core.JobKindNode, GraphRunID: "run-new", NodeID: "http",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusSucceeded,
		EnqueuedAt: now, FinishedAt: fin(0),
		Result: &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"body": {Inline: "hi"}}},
	}, {
		ID: "parked", Kind: core.JobKindNode, GraphRunID: "run-parked", NodeID: "gate",
		Tenant: "acme", Workspace: "default", Status: core.JobStatusAwaiting,
		EnqueuedAt: now,
		Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
			"pending_url": {Inline: "u"},
		}},
	}}
	for _, r := range recs {
		if err := store.Enqueue(ctx, r); err != nil {
			t.Fatalf("enqueue %s: %v", r.ID, err)
		}
		// Enqueuing a record that is already terminal stamps finished_at with
		// the clock — the memory store honors a supplied one, Postgres does
		// not — so the store under test puts the fixture's own finish times
		// back. Controlling them is the whole point here: the ordering this
		// exercises is only visible when "finished last" and "enqueued last"
		// disagree.
		if r.FinishedAt != nil && setFinished != nil {
			setFinished(r.ID, *r.FinishedAt)
		}
	}
}

// checkApprovalFilters is the shared assertion set: the same query must mean
// the same thing in memory and in Postgres, or the approvals page shows one
// thing in tests and another in production.
func checkApprovalFilters(t *testing.T, store core.JobStore) {
	t.Helper()
	ctx := context.Background()

	decided, err := store.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant: "acme", Workspace: "default", Status: core.JobStatusSucceeded,
		HasOutputPort: "pending_url", NewestByFinished: true,
	})
	if err != nil {
		t.Fatalf("list decided: %v", err)
	}
	ids := make([]string, 0, len(decided))
	for _, r := range decided {
		ids = append(ids, r.ID)
	}
	// The ordinary step is excluded by the port filter; the parked one by the
	// status; and the long-parked decision leads because it finished last.
	if len(ids) != 2 || ids[0] != "old-decided" || ids[1] != "new-decided" {
		t.Fatalf("decided = %v, want [old-decided new-decided]", ids)
	}

	// The same port filter, on the awaiting side, is the inbox query.
	parked, err := store.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant: "acme", Workspace: "default", Status: core.JobStatusAwaiting,
		HasOutputPort: "pending_url",
	})
	if err != nil {
		t.Fatalf("list parked: %v", err)
	}
	if len(parked) != 1 || parked[0].ID != "parked" {
		t.Fatalf("parked = %+v, want the one awaiting approval", parked)
	}

	// A port nothing carries returns nothing, rather than everything.
	none, err := store.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant: "acme", HasOutputPort: "no_such_port",
	})
	if err != nil {
		t.Fatalf("list none: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("unknown port matched %d records", len(none))
	}

	// Under a limit, the ordering decides WHICH row comes back.
	one, err := store.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant: "acme", Workspace: "default", Status: core.JobStatusSucceeded,
		HasOutputPort: "pending_url", NewestByFinished: true, Limit: 1,
	})
	if err != nil {
		t.Fatalf("list limit: %v", err)
	}
	if len(one) != 1 || one[0].ID != "old-decided" {
		t.Errorf("limit=1 = %+v, want the most recently decided", one)
	}
}

func TestMemory_ApprovalFilters(t *testing.T) {
	store := NewMemory()
	approvalFixture(t, store, nil) // Enqueue keeps the supplied finish times
	checkApprovalFilters(t, store)
}

func TestPostgres_ApprovalFilters(t *testing.T) {
	url := testDB(t)
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()
	_, _ = store.pool.Exec(ctx, "TRUNCATE jobs")

	approvalFixture(t, store, func(id string, at time.Time) {
		if _, err := store.pool.Exec(ctx,
			"UPDATE jobs SET finished_at = $2 WHERE id = $1", id, at); err != nil {
			t.Fatalf("set finished_at on %s: %v", id, err)
		}
	})
	checkApprovalFilters(t, store)
}
