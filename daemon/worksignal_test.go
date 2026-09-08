// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"sync"
	"testing"
	"time"
)

// Pins the ordering property the worker
// loop depends on: a waiter captured BEFORE the enqueue still sees it.
//
// This is the whole correctness argument for cutting a poll short. The worker
// takes its waiter, then claims, and only waits if the claim came back empty —
// so a Notify racing in between must still wake it. Take the waiter after the
// claim instead and that Notify is lost, the worker sleeps out its full
// interval with work already queued, and the stall this exists to remove comes
// back as a race that only appears under load.
func TestWorkSignalWaiterTakenBeforeNotify(t *testing.T) {
	s := NewWorkSignal()
	w := s.Waiter() // before
	s.Notify()
	select {
	case <-w:
	default:
		t.Fatal("a waiter taken before Notify was not woken by it")
	}
}

// TestWorkSignalWakesEveryWaiter: Notify is a broadcast, because an enqueue is
// usually several ready steps and waking one worker per step would drain a
// fan-out serially.
func TestWorkSignalWakesEveryWaiter(t *testing.T) {
	s := NewWorkSignal()
	const waiters = 8
	var wg sync.WaitGroup
	wg.Add(waiters)
	for range waiters {
		w := s.Waiter()
		go func() { defer wg.Done(); <-w }()
	}
	time.Sleep(10 * time.Millisecond)
	s.Notify()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify did not wake every waiter")
	}
}

// TestWorkSignalSecondWaiterIsFresh: a waiter taken after a Notify must not
// already be closed, or a worker would spin on a stale wake instead of
// waiting — turning an idle poll into a busy loop against the job store.
func TestWorkSignalSecondWaiterIsFresh(t *testing.T) {
	s := NewWorkSignal()
	s.Notify()
	select {
	case <-s.Waiter():
		t.Fatal("waiter taken after Notify was already closed")
	default:
	}
}

// TestWorkSignalNilIsPollOnly: every wiring that does not opt in must keep
// working, which means a nil signal is inert and its waiter never fires — the
// caller falls back to its poll timer.
func TestWorkSignalNilIsPollOnly(t *testing.T) {
	var s *WorkSignal
	s.Notify() // must not panic
	select {
	case <-s.Waiter():
		t.Fatal("nil WorkSignal produced a live waiter")
	case <-time.After(20 * time.Millisecond):
	}
}
