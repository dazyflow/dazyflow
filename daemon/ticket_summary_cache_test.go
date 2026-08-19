// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// The QueueSummary cache promises two things that only show up under precise
// interleaving: an agent's own write is visible on their next read, and one bad
// scan doesn't wedge the cache for the life of the process. Both are driven
// through summaryCompute, so no Postgres is needed — the cache logic under test
// never touches the pool.

func summaryWith(n int) core.TicketQueueSummary {
	sum := core.NewTicketQueueSummary()
	sum.Add(core.TicketAwaitingSupport, "", n)
	return sum
}

// awaiting pulls the one count these tests vary, whatever shape the summary has.
func awaiting(t *testing.T, sum core.TicketQueueSummary) int {
	t.Helper()
	return sum.ByStatus[core.TicketAwaitingSupport]
}

func TestQueueSummaryCache_InvalidationDuringScanIsNotLost(t *testing.T) {
	ctx := context.Background()
	s := &PgTicketStore{}

	// A scan we can hold open, so a write can land in the middle of it.
	started := make(chan struct{})
	release := make(chan struct{})
	s.summaryCompute = func(context.Context) (core.TicketQueueSummary, error) {
		close(started)
		<-release
		return summaryWith(1), nil // the PRE-write count
	}

	done := make(chan core.TicketQueueSummary, 1)
	go func() {
		sum, err := s.QueueSummary(ctx)
		if err != nil {
			t.Errorf("scan: %v", err)
		}
		done <- sum
	}()
	<-started
	// The agent claims a ticket while the scan is in flight.
	s.invalidateSummary()
	close(release)
	<-done

	// That stale snapshot must NOT have been cached: the next read re-scans and
	// sees the claim. Before the generation counter it served 1 for the whole
	// 5s TTL — the agent's own action invisible.
	s.summaryCompute = func(context.Context) (core.TicketQueueSummary, error) {
		return summaryWith(2), nil // the POST-write count
	}
	sum, err := s.QueueSummary(ctx)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if got := awaiting(t, sum); got != 2 {
		t.Errorf("awaiting = %d, want 2 — a scan that predated the write was cached", got)
	}
}

func TestQueueSummaryCache_PanicDoesNotWedgeTheCache(t *testing.T) {
	ctx := context.Background()
	s := &PgTicketStore{}

	s.summaryCompute = func(context.Context) (core.TicketQueueSummary, error) {
		panic("scan blew up")
	}
	func() {
		// The HTTP middleware recovers this in production; the process lives on.
		defer func() { _ = recover() }()
		_, _ = s.QueueSummary(ctx)
	}()

	// The in-flight flag must have been cleared on the way out, or every later
	// caller takes the "someone else is scanning" branch forever.
	s.summaryMu.Lock()
	inFlight := s.summaryInFlgt
	s.summaryMu.Unlock()
	if inFlight {
		t.Fatal("summaryInFlgt stayed set after a panic — the cache is wedged")
	}
	s.summaryCompute = func(context.Context) (core.TicketQueueSummary, error) {
		return summaryWith(7), nil
	}
	sum, err := s.QueueSummary(ctx)
	if err != nil {
		t.Fatalf("read after panic: %v", err)
	}
	if got := awaiting(t, sum); got != 7 {
		t.Errorf("awaiting = %d, want 7", got)
	}
}

func TestQueueSummaryCache_ErrorIsNotCached(t *testing.T) {
	ctx := context.Background()
	s := &PgTicketStore{}
	boom := errors.New("db down")
	s.summaryCompute = func(context.Context) (core.TicketQueueSummary, error) {
		return core.NewTicketQueueSummary(), boom
	}
	if _, err := s.QueueSummary(ctx); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	s.summaryCompute = func(context.Context) (core.TicketQueueSummary, error) {
		return summaryWith(3), nil
	}
	sum, err := s.QueueSummary(ctx)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := awaiting(t, sum); got != 3 {
		t.Errorf("awaiting = %d, want 3 — a failed scan must not be cached", got)
	}
}
