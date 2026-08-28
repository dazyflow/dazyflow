// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Shared store-conformance tests for the missing functions. Both Memory
// and Postgres run the same bodies via runConformance — every method on
// core.JobStore that wasn't exercised by the original per-store tests is
// pinned here once, and runs against both stores.

func runConformance(t *testing.T, mk func(t *testing.T) core.JobStore) {
	t.Helper()
	t.Run("CountsByStatus", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		// Seed: 2 queued, 1 running, 1 succeeded, 1 failed (node-kind).
		// Plus a graph-kind row that must be excluded from the count.
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "q1", Kind: core.JobKindNode, Status: core.JobStatusQueued, Tenant: "t"})
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "q2", Kind: core.JobKindNode, Status: core.JobStatusQueued, Tenant: "t"})
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "r1", Kind: core.JobKindNode, Status: core.JobStatusRunning, Tenant: "t"})
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "ok1", Kind: core.JobKindNode, Status: core.JobStatusQueued, Tenant: "t"})
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "fail1", Kind: core.JobKindNode, Status: core.JobStatusQueued, Tenant: "t"})
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "graph-row", Kind: core.JobKindGraph, Status: core.JobStatusRunning, Tenant: "t"})
		// Move ok1 and fail1 to terminal.
		if err := s.Complete(ctx, "ok1", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
			t.Fatalf("Complete ok1: %v", err)
		}
		if err := s.Complete(ctx, "fail1", core.JobStatusFailed, &core.Result{Status: core.StatusError}); err != nil {
			t.Fatalf("Complete fail1: %v", err)
		}

		counter, ok := s.(core.JobCounter)
		if !ok {
			t.Skip("store does not implement JobCounter")
		}
		counts, err := counter.CountsByStatus(ctx)
		if err != nil {
			t.Fatalf("CountsByStatus: %v", err)
		}
		if counts[core.JobStatusQueued] != 2 {
			t.Errorf("queued = %d, want 2", counts[core.JobStatusQueued])
		}
		if counts[core.JobStatusRunning] != 1 {
			t.Errorf("running = %d, want 1", counts[core.JobStatusRunning])
		}
		if counts[core.JobStatusSucceeded] != 1 {
			t.Errorf("succeeded = %d, want 1", counts[core.JobStatusSucceeded])
		}
		if counts[core.JobStatusFailed] != 1 {
			t.Errorf("failed = %d, want 1", counts[core.JobStatusFailed])
		}
		// graph-kind row must not leak in.
		total := 0
		for _, n := range counts {
			total += n
		}
		if total != 5 {
			t.Errorf("total node-kind = %d, want 5 (graph row leaked?)", total)
		}
	})

	t.Run("Requeue_success", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "j", Kind: core.JobKindNode, Tenant: "t"})
		if _, err := s.Claim(ctx, "w", time.Minute); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		future := time.Now().Add(time.Hour)
		if err := s.Requeue(ctx, "j", future); err != nil {
			t.Fatalf("Requeue: %v", err)
		}
		got, err := s.Get(ctx, "j")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != core.JobStatusQueued {
			t.Errorf("status = %q, want queued", got.Status)
		}
		if got.LeaseUntil != nil {
			t.Errorf("lease should be cleared after requeue")
		}
		if got.AvailableAt == nil || got.AvailableAt.Sub(future).Abs() > time.Second {
			t.Errorf("available_at = %v, want ~%v", got.AvailableAt, future)
		}
	})

	t.Run("Requeue_terminal_rejected", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "j", Kind: core.JobKindNode, Tenant: "t"})
		if err := s.Complete(ctx, "j", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		err := s.Requeue(ctx, "j", time.Now())
		if !errors.Is(err, core.ErrConflict) {
			t.Errorf("Requeue terminal = %v, want ErrConflict", err)
		}
	})

	t.Run("Requeue_missing", func(t *testing.T) {
		s := mk(t)
		err := s.Requeue(t.Context(), "no-such-id", time.Now())
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Requeue missing = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListGraphRuns_filters_and_pagination", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		// Seed: 3 graph rows for tenant acme/ws-prod under graph-A in
		// alternating statuses, 1 for ws-dev, 1 for globex. Plus a
		// node-kind row that must be filtered out.
		now := time.Now()
		mk2 := func(id, tenant, ws, gid string, status core.JobStatus, offset time.Duration) {
			mustEnqueue(t, s, ctx, core.JobRecord{
				ID: id, Kind: core.JobKindGraph, Status: status,
				Tenant: tenant, Workspace: ws, GraphID: gid,
				EnqueuedAt: now.Add(offset),
			})
		}
		mk2("g1", "acme", "ws-prod", "graph-A", core.JobStatusRunning, -3*time.Minute)
		mk2("g2", "acme", "ws-prod", "graph-A", core.JobStatusSucceeded, -2*time.Minute)
		mk2("g3", "acme", "ws-prod", "graph-A", core.JobStatusFailed, -time.Minute)
		mk2("g4", "acme", "ws-dev", "graph-A", core.JobStatusSucceeded, -90*time.Second)
		mk2("g5", "globex", "ws-prod", "graph-B", core.JobStatusSucceeded, -45*time.Second)
		mustEnqueue(t, s, ctx, core.JobRecord{
			ID: "n1", Kind: core.JobKindNode, Tenant: "acme", Workspace: "ws-prod", GraphID: "graph-A",
			EnqueuedAt: now,
		})

		// Tenant filter narrows.
		got, err := s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "acme"})
		if err != nil {
			t.Fatalf("ListGraphRuns: %v", err)
		}
		if len(got) != 4 {
			t.Errorf("acme rows = %d, want 4 (got %v)", len(got), ids(got))
		}
		for _, r := range got {
			if r.Kind != core.JobKindGraph {
				t.Errorf("non-graph kind leaked: %v", r)
			}
		}

		// Workspace narrows further.
		got, err = s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "acme", Workspace: "ws-prod"})
		if err != nil {
			t.Fatalf("ListGraphRuns ws filter: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("acme/ws-prod rows = %d, want 3", len(got))
		}

		// Status filter.
		got, _ = s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "acme", Status: core.JobStatusSucceeded})
		if len(got) != 2 {
			t.Errorf("acme succeeded = %d, want 2", len(got))
		}

		// GraphID filter.
		got, _ = s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "globex", GraphID: "graph-B"})
		if len(got) != 1 || got[0].ID != "g5" {
			t.Errorf("globex/graph-B rows = %v, want [g5]", ids(got))
		}

		// Sorted DESC by EnqueuedAt.
		got, _ = s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "acme", Workspace: "ws-prod"})
		if got[0].ID != "g3" || got[len(got)-1].ID != "g1" {
			t.Errorf("order = %v, want g3..g1 (desc)", ids(got))
		}

		// Limit clamps to N.
		got, _ = s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "acme", Limit: 2})
		if len(got) != 2 {
			t.Errorf("limit=2 → %d rows", len(got))
		}

		// Offset skips the newest.
		gotAll, _ := s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "acme"})
		gotOff, _ := s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "acme", Offset: 2})
		if len(gotOff) != len(gotAll)-2 {
			t.Errorf("offset=2 → %d rows, want %d", len(gotOff), len(gotAll)-2)
		}
		if len(gotOff) > 0 && gotOff[0].ID != gotAll[2].ID {
			t.Errorf("offset=2 starts with %q, want %q", gotOff[0].ID, gotAll[2].ID)
		}

		// Offset beyond end → empty, not error.
		gotEmpty, err := s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "acme", Offset: 99})
		if err != nil {
			t.Errorf("offset-past-end err = %v", err)
		}
		if len(gotEmpty) != 0 {
			t.Errorf("offset-past-end len = %d, want 0", len(gotEmpty))
		}

		// Date range over EnqueuedAt. acme/ws-prod rows sit at now-3m (g1),
		// now-2m (g2), now-1m (g3). Since is inclusive, Until exclusive.
		// [now-2m30s, now-90s) selects exactly g2 (now-2m).
		got, _ = s.ListGraphRuns(ctx, core.ListGraphRunsOpts{
			Tenant: "acme", Workspace: "ws-prod",
			Since: now.Add(-150 * time.Second),
			Until: now.Add(-90 * time.Second),
		})
		if len(got) != 1 || got[0].ID != "g2" {
			t.Errorf("date range = %v, want [g2]", ids(got))
		}
		// Since alone (inclusive) keeps g2 and g3, drops the older g1.
		got, _ = s.ListGraphRuns(ctx, core.ListGraphRunsOpts{
			Tenant: "acme", Workspace: "ws-prod",
			Since: now.Add(-2 * time.Minute),
		})
		if len(got) != 2 {
			t.Errorf("since-only = %v, want [g3 g2]", ids(got))
		}
		// Until is exclusive: an upper bound exactly at g1's enqueue time
		// excludes g1, leaving nothing older.
		got, _ = s.ListGraphRuns(ctx, core.ListGraphRunsOpts{
			Tenant: "acme", Workspace: "ws-prod",
			Until: now.Add(-3 * time.Minute),
		})
		if len(got) != 0 {
			t.Errorf("until-exclusive at g1 = %v, want []", ids(got))
		}

		// Pagination tiebreaker: rows that share an enqueued_at must order
		// deterministically (by id DESC) so a page boundary can't drop or repeat
		// a row. Seed 5 runs at the SAME instant, page through with limit=2, and
		// assert the concatenated pages are the full set with no dup/gap.
		tie := now.Add(-10 * time.Minute)
		for _, id := range []string{"tie-1", "tie-3", "tie-2", "tie-5", "tie-4"} {
			mustEnqueue(t, s, ctx, core.JobRecord{
				ID: id, Kind: core.JobKindGraph, Status: core.JobStatusSucceeded,
				Tenant: "tieco", Workspace: "ws", GraphID: "g", EnqueuedAt: tie,
			})
		}
		var paged []string
		for off := 0; off < 6; off += 2 {
			page, err := s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "tieco", Limit: 2, Offset: off})
			if err != nil {
				t.Fatalf("tie page off=%d: %v", off, err)
			}
			paged = append(paged, ids(page)...)
		}
		seen := map[string]int{}
		for _, id := range paged {
			seen[id]++
		}
		if len(seen) != 5 {
			t.Errorf("paginated tied rows = %v, want 5 distinct (no dup/gap across pages)", paged)
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("row %q appeared %d times across pages, want 1", id, n)
			}
		}
		// And the order must be stable id-DESC within the tie.
		first, _ := s.ListGraphRuns(ctx, core.ListGraphRunsOpts{Tenant: "tieco", Limit: 5})
		want := []string{"tie-5", "tie-4", "tie-3", "tie-2", "tie-1"}
		for i, w := range want {
			if i < len(first) && first[i].ID != w {
				t.Errorf("tie order[%d] = %q, want %q (id DESC)", i, first[i].ID, w)
			}
		}
	})

	t.Run("ListNodeRecords_filters_and_pagination", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		now := time.Now()
		mk2 := func(id, tenant, ws, runID string, status core.JobStatus, offset time.Duration) {
			mustEnqueue(t, s, ctx, core.JobRecord{
				ID: id, Kind: core.JobKindNode, Status: status,
				Tenant: tenant, Workspace: ws, GraphRunID: runID,
				EnqueuedAt: now.Add(offset),
			})
		}
		mk2("a", "acme", "ws-prod", "run-1", core.JobStatusQueued, -3*time.Minute)
		mk2("b", "acme", "ws-prod", "run-1", core.JobStatusRunning, -2*time.Minute)
		mk2("c", "acme", "ws-prod", "run-2", core.JobStatusQueued, -time.Minute)
		mk2("d", "acme", "ws-dev", "run-3", core.JobStatusQueued, -90*time.Second)
		mk2("e", "globex", "ws-prod", "run-4", core.JobStatusQueued, -45*time.Second)
		// graph-kind row must not appear.
		mustEnqueue(t, s, ctx, core.JobRecord{
			ID: "graph", Kind: core.JobKindGraph, Tenant: "acme", Workspace: "ws-prod",
			GraphRunID: "run-1", EnqueuedAt: now,
		})

		// Tenant filter.
		got, err := s.ListNodeRecords(ctx, core.ListNodeRecordsOpts{Tenant: "acme"})
		if err != nil {
			t.Fatalf("ListNodeRecords: %v", err)
		}
		if len(got) != 4 {
			t.Errorf("acme node rows = %d, want 4 (got %v)", len(got), ids(got))
		}
		for _, r := range got {
			if r.Kind != core.JobKindNode {
				t.Errorf("non-node kind leaked: %v", r)
			}
		}

		// Workspace + Status.
		got, _ = s.ListNodeRecords(ctx, core.ListNodeRecordsOpts{Tenant: "acme", Workspace: "ws-prod", Status: core.JobStatusQueued})
		if len(got) != 2 {
			t.Errorf("acme/ws-prod queued = %d, want 2", len(got))
		}

		// GraphRunID narrows to one run.
		got, _ = s.ListNodeRecords(ctx, core.ListNodeRecordsOpts{Tenant: "acme", GraphRunID: "run-1"})
		if len(got) != 2 {
			t.Errorf("acme run-1 rows = %d, want 2", len(got))
		}

		// Sort DESC by enqueued_at. Newest first: c (-1m), d (-90s), b (-2m), a (-3m).
		got, _ = s.ListNodeRecords(ctx, core.ListNodeRecordsOpts{Tenant: "acme"})
		want := []string{"c", "d", "b", "a"}
		if !sameIDs(got, want) {
			t.Errorf("order = %v, want %v", ids(got), want)
		}

		// Limit.
		got, _ = s.ListNodeRecords(ctx, core.ListNodeRecordsOpts{Tenant: "acme", Limit: 1})
		if len(got) != 1 {
			t.Errorf("limit=1 → %d", len(got))
		}

		// Offset past end.
		gotEmpty, err := s.ListNodeRecords(ctx, core.ListNodeRecordsOpts{Tenant: "acme", Offset: 99})
		if err != nil {
			t.Errorf("offset-past-end err = %v", err)
		}
		if len(gotEmpty) != 0 {
			t.Errorf("offset-past-end len = %d, want 0", len(gotEmpty))
		}
	})

	t.Run("Renew_missing_or_unowned", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		// Missing record.
		err := s.Renew(ctx, "no-such-id", "w", time.Minute)
		// pg conflates "missing" with "unowned" via RowsAffected==0 (returns
		// ErrConflict); memory returns ErrNotFound. Either is acceptable.
		if !errors.Is(err, core.ErrNotFound) && !errors.Is(err, core.ErrConflict) {
			t.Errorf("Renew missing = %v, want ErrNotFound or ErrConflict", err)
		}
		// Unowned (different worker).
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "j", Kind: core.JobKindNode, Tenant: "t"})
		if _, err := s.Claim(ctx, "owner", time.Minute); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		err = s.Renew(ctx, "j", "thief", time.Minute)
		if !errors.Is(err, core.ErrConflict) {
			t.Errorf("Renew unowned = %v, want ErrConflict", err)
		}
		// Owned renew succeeds and extends the lease.
		if err := s.Renew(ctx, "j", "owner", 2*time.Minute); err != nil {
			t.Errorf("Renew owned: %v", err)
		}
	})

	t.Run("Complete_missing_returns_NotFound", func(t *testing.T) {
		s := mk(t)
		err := s.Complete(t.Context(), "ghost", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK})
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Complete missing = %v, want ErrNotFound", err)
		}
	})

	t.Run("Complete_terminal_rejected", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "j", Kind: core.JobKindNode, Tenant: "t"})
		if err := s.Complete(ctx, "j", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		// Second Complete must be rejected as a conflict.
		err := s.Complete(ctx, "j", core.JobStatusFailed, &core.Result{Status: core.StatusError})
		if !errors.Is(err, core.ErrConflict) {
			t.Errorf("re-Complete = %v, want ErrConflict", err)
		}
	})

	t.Run("Complete_awaiting_then_resume", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "j", Kind: core.JobKindNode, Tenant: "t"})
		// Awaiting parks the record without finishing it.
		if err := s.Complete(ctx, "j", core.JobStatusAwaiting, &core.Result{Status: core.StatusAwaiting}); err != nil {
			t.Fatalf("Complete awaiting: %v", err)
		}
		rec, _ := s.Get(ctx, "j")
		if rec.Status != core.JobStatusAwaiting {
			t.Errorf("status after awaiting = %q", rec.Status)
		}
		if rec.FinishedAt != nil {
			t.Errorf("finished_at must stay nil while awaiting")
		}
		// Resume to a terminal status.
		if err := s.Complete(ctx, "j", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
			t.Fatalf("resume Complete: %v", err)
		}
		rec, _ = s.Get(ctx, "j")
		if rec.Status != core.JobStatusSucceeded {
			t.Errorf("status after resume = %q, want succeeded", rec.Status)
		}
		if rec.FinishedAt == nil {
			t.Errorf("finished_at must be set after terminal write")
		}
	})

	// A node that already parked must not be able to park again: the only
	// way to reach a second park is a second execution of the same record
	// (expired lease reclaimed mid-run), and the daemon mails the approvers
	// off the back of every park that commits. Both stores must report the
	// re-park as ErrConflict so the worker abandons instead of notifying.
	t.Run("Complete_awaiting_twice_rejected", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "j", Kind: core.JobKindNode, Tenant: "t"})
		if err := s.Complete(ctx, "j", core.JobStatusAwaiting, &core.Result{Status: core.StatusAwaiting}); err != nil {
			t.Fatalf("first park: %v", err)
		}
		err := s.Complete(ctx, "j", core.JobStatusAwaiting, &core.Result{Status: core.StatusAwaiting})
		if !errors.Is(err, core.ErrConflict) {
			t.Errorf("re-park = %v, want ErrConflict", err)
		}
		// The first park's record must survive the rejected write, and the
		// resume path must still work afterwards.
		rec, _ := s.Get(ctx, "j")
		if rec.Status != core.JobStatusAwaiting {
			t.Errorf("status after rejected re-park = %q, want awaiting", rec.Status)
		}
		if err := s.Complete(ctx, "j", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
			t.Errorf("resume after rejected re-park: %v", err)
		}
	})

	t.Run("Complete_nonterminal_rejected", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "j", Kind: core.JobKindNode, Tenant: "t"})
		// Writing a non-terminal, non-awaiting status is a programming
		// error — must be rejected.
		err := s.Complete(ctx, "j", core.JobStatusQueued, nil)
		if !errors.Is(err, core.ErrConflict) {
			t.Errorf("Complete to queued = %v, want ErrConflict", err)
		}
	})

	t.Run("Get_missing_returns_NotFound", func(t *testing.T) {
		s := mk(t)
		_, err := s.Get(t.Context(), "no-such-id")
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Get missing = %v, want ErrNotFound", err)
		}
	})

	t.Run("Enqueue_duplicate_rejected", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "dupe", Kind: core.JobKindNode, Tenant: "t"})
		err := s.Enqueue(ctx, core.JobRecord{ID: "dupe", Kind: core.JobKindNode, Tenant: "t"})
		if err == nil {
			t.Errorf("duplicate Enqueue accepted (want error)")
		}
		// Assert the SENTINEL, not just non-nil. Asserting only err != nil is
		// what let the two backends drift: Memory returned core.ErrConflict
		// and Postgres returned an opaque wrap of SQLSTATE 23505, so callers
		// branching on errors.Is(err, core.ErrConflict) behaved differently
		// depending on which store was configured.
		if !errors.Is(err, core.ErrConflict) {
			t.Errorf("duplicate Enqueue = %v, want core.ErrConflict", err)
		}
	})

	t.Run("Enqueue_preserves_seed_result", func(t *testing.T) {
		// A seeded node (SubmitGraphWithSeed pre-completing a
		// webhook_input/trigger) is Enqueued already-succeeded WITH a
		// Result. Get must return that Result intact — Postgres used to
		// drop it (the INSERT omitted the result column), leaving the
		// trigger succeeded-but-result-less so a downstream node's
		// load_predecessors failed with "predecessor has no result yet".
		s := mk(t)
		ctx := t.Context()
		seed := core.JobRecord{
			ID: "seed-1", Kind: core.JobKindNode, GraphID: "g1", NodeID: "trigger", Tenant: "t",
			Status: core.JobStatusSucceeded,
			Result: &core.Result{
				Status: core.StatusOK,
				Output: map[string]core.Ref{
					"body": {MIME: "application/json", Inline: map[string]any{"email": "ada@example.com"}},
				},
			},
		}
		mustEnqueue(t, s, ctx, seed)
		got, err := s.Get(ctx, "seed-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != core.JobStatusSucceeded {
			t.Errorf("status = %q, want succeeded", got.Status)
		}
		if got.Result == nil {
			t.Fatal("Result is nil — seed result was dropped on Enqueue")
		}
		ref, ok := got.Result.Output["body"]
		if !ok {
			t.Fatalf("Result.Output missing 'body': %+v", got.Result.Output)
		}
		body, ok := ref.Inline.(map[string]any)
		if !ok || body["email"] != "ada@example.com" {
			t.Errorf("body = %#v, want {email: ada@example.com}", ref.Inline)
		}
	})

	t.Run("ListByGraph", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		for _, id := range []string{"a", "b", "c"} {
			mustEnqueue(t, s, ctx, core.JobRecord{ID: id, Kind: core.JobKindNode, GraphID: "g1", Tenant: "t"})
		}
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "z", Kind: core.JobKindNode, GraphID: "other", Tenant: "t"})
		got, err := s.ListByGraph(ctx, "g1")
		if err != nil {
			t.Fatalf("ListByGraph: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("len = %d, want 3", len(got))
		}
		for _, r := range got {
			if r.GraphID != "g1" {
				t.Errorf("graph = %q, want g1", r.GraphID)
			}
		}
	})

	t.Run("DeleteByTenant", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		d, ok := s.(interface {
			DeleteByTenant(context.Context, string) (int, error)
		})
		if !ok {
			t.Skip("store does not implement DeleteByTenant")
		}
		// Two tenants; deletion of one leaves the other intact.
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "a1", Kind: core.JobKindNode, Tenant: "acme"})
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "a2", Kind: core.JobKindGraph, Tenant: "acme"})
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "g1", Kind: core.JobKindNode, Tenant: "globex"})

		n, err := d.DeleteByTenant(ctx, "acme")
		if err != nil {
			t.Fatalf("DeleteByTenant: %v", err)
		}
		if n != 2 {
			t.Errorf("deleted = %d, want 2", n)
		}
		if _, err := s.Get(ctx, "a1"); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("a1 still present after delete: %v", err)
		}
		if _, err := s.Get(ctx, "g1"); err != nil {
			t.Errorf("globex row should survive: %v", err)
		}
		// Deleting a tenant with no rows is a no-op, not an error.
		if n, err := d.DeleteByTenant(ctx, "nobody"); err != nil || n != 0 {
			t.Errorf("DeleteByTenant(empty) = %d, %v; want 0, nil", n, err)
		}
	})

	t.Run("SetGraphRunParked", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		parker, ok := s.(core.GraphRunParker)
		if !ok {
			t.Skip("store does not implement GraphRunParker")
		}
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "gr", Kind: core.JobKindGraph, Status: core.JobStatusRunning, Tenant: "t"})

		// running → awaiting, once.
		did, err := parker.SetGraphRunParked(ctx, "gr", true)
		if err != nil || !did {
			t.Fatalf("park = %v, %v; want true, nil", did, err)
		}
		if got, _ := s.Get(ctx, "gr"); got.Status != core.JobStatusAwaiting {
			t.Errorf("status = %q, want awaiting", got.Status)
		}
		// A second park is a no-op — this is the two-steps-parked-at-once
		// case, and the caller must not have to count outstanding pauses.
		if did, err := parker.SetGraphRunParked(ctx, "gr", true); err != nil || did {
			t.Errorf("second park = %v, %v; want false, nil", did, err)
		}

		// awaiting → running, once.
		did, err = parker.SetGraphRunParked(ctx, "gr", false)
		if err != nil || !did {
			t.Fatalf("unpark = %v, %v; want true, nil", did, err)
		}
		if got, _ := s.Get(ctx, "gr"); got.Status != core.JobStatusRunning {
			t.Errorf("status = %q, want running", got.Status)
		}
		if did, err := parker.SetGraphRunParked(ctx, "gr", false); err != nil || did {
			t.Errorf("second unpark = %v, %v; want false, nil", did, err)
		}

		// A terminal run is never dragged back: a park racing a cancel must
		// lose, or the run would report "waiting for approval" forever with
		// nothing left to decide it.
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "done", Kind: core.JobKindGraph, Status: core.JobStatusRunning, Tenant: "t"})
		if err := s.Complete(ctx, "done", core.JobStatusCancelled, nil); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if did, err := parker.SetGraphRunParked(ctx, "done", true); err != nil || did {
			t.Errorf("park(terminal) = %v, %v; want false, nil", did, err)
		}
		if got, _ := s.Get(ctx, "done"); got.Status != core.JobStatusCancelled {
			t.Errorf("terminal status changed to %q", got.Status)
		}

		// Node-kind rows are never touched — awaiting means something else
		// there (a parked step), and this is the run-level status.
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "nd", Kind: core.JobKindNode, Status: core.JobStatusRunning, Tenant: "t"})
		if did, err := parker.SetGraphRunParked(ctx, "nd", true); err != nil || did {
			t.Errorf("park(node) = %v, %v; want false, nil", did, err)
		}

		// Missing rows: same tolerated divergence as MarkGraphRunning.
		did, err = parker.SetGraphRunParked(ctx, "ghost", true)
		if did {
			t.Error("park(missing) reported a transition")
		}
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			t.Errorf("park(missing) = %v, want nil or ErrNotFound", err)
		}
	})

	t.Run("MarkGraphRunning", func(t *testing.T) {
		s := mk(t)
		ctx := t.Context()
		starter, ok := s.(core.GraphRunStarter)
		if !ok {
			t.Skip("store does not implement GraphRunStarter")
		}
		// A queued graph row flips to running exactly once.
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "gr", Kind: core.JobKindGraph, Status: core.JobStatusQueued, Tenant: "t"})
		did, err := starter.MarkGraphRunning(ctx, "gr")
		if err != nil || !did {
			t.Fatalf("first MarkGraphRunning = %v, %v; want true, nil", did, err)
		}
		got, _ := s.Get(ctx, "gr")
		if got.Status != core.JobStatusRunning {
			t.Errorf("status = %q, want running", got.Status)
		}
		if got.StartedAt == nil {
			t.Error("StartedAt should be set after MarkGraphRunning")
		}
		// A second call is a no-op (already running): returns false.
		if did, err := starter.MarkGraphRunning(ctx, "gr"); err != nil || did {
			t.Errorf("second MarkGraphRunning = %v, %v; want false, nil", did, err)
		}
		// A node-kind row is never promoted.
		mustEnqueue(t, s, ctx, core.JobRecord{ID: "nd", Kind: core.JobKindNode, Status: core.JobStatusQueued, Tenant: "t"})
		if did, err := starter.MarkGraphRunning(ctx, "nd"); err != nil || did {
			t.Errorf("MarkGraphRunning(node) = %v, %v; want false, nil", did, err)
		}
		// A missing row is not promoted. Memory returns ErrNotFound;
		// Postgres conflates missing with already-running via a conditional
		// UPDATE (RowsAffected 0 → false, nil). Either is acceptable, but
		// it must never report a successful transition.
		did, err = starter.MarkGraphRunning(ctx, "ghost")
		if did {
			t.Error("MarkGraphRunning(missing) reported a transition")
		}
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			t.Errorf("MarkGraphRunning(missing) = %v, want nil or ErrNotFound", err)
		}
	})
}

func mustEnqueue(t *testing.T, s core.JobStore, ctx context.Context, rec core.JobRecord) {
	t.Helper()
	if err := s.Enqueue(ctx, rec); err != nil {
		t.Fatalf("Enqueue %s: %v", rec.ID, err)
	}
}

func ids(recs []core.JobRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func sameIDs(got []core.JobRecord, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i, r := range got {
		if r.ID != want[i] {
			return false
		}
	}
	return true
}

// TestMemory_Conformance runs the shared conformance suite against the
// in-memory store. Always runs (no DB needed).
func TestMemory_Conformance(t *testing.T) {
	runConformance(t, func(t *testing.T) core.JobStore { return NewMemory() })
}
