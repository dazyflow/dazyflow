// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"sync"
	"sync/atomic"
)

// WorkSignal wakes idle workers the moment work is enqueued, instead of
// leaving them to discover it on their next poll.
//
// Why this exists: a worker that finds an empty queue sleeps for
// WorkerConfig.PollInterval (100ms in production). That costs nothing at
// saturation — the queue is never empty, so the sleep branch is never reached,
// which is exactly why the stress rig cannot see it — but on an idle fleet it
// is the whole of a run's start-up latency. Measured before this landed
// (TestRunLatencyByPollInterval): a one-step run took 100.9ms end to end for
// ~0ms of actual work, and a twelve-step run took 81.5ms, because after the
// first claim a worker goes straight back for the successor it just enqueued.
// Nearly all of it was one sleep at the front.
//
// Idle workers also SYNCHRONIZE — they wake together, all find nothing, and
// sleep together — so the wait is not the half-interval an independent-phase
// model would predict. It sits near the full one.
//
// This is an optimization, never the mechanism work is found by. The poll
// remains, and remains the backstop that covers everything a signal cannot:
// work enqueued by another replica, a retry whose backoff expires, a lease
// that lapses. A missed Notify costs one poll interval, not a stuck run.
type WorkSignal struct {
	// ch is read on the claim loop's hot path — once per iteration, by every
	// worker, at saturation — so the read side is an atomic load and takes no
	// lock. A mutex here would put a single process-wide lock in front of
	// every step the fleet executes, to serve a wait that only idle workers
	// ever perform. The mutex below serializes Notify against itself only.
	ch atomic.Pointer[chan struct{}]
	mu sync.Mutex
}

func NewWorkSignal() *WorkSignal {
	s := &WorkSignal{}
	c := make(chan struct{})
	s.ch.Store(&c)
	return s
}

// Waiter returns a channel that the next Notify closes.
//
// CAPTURE IT BEFORE THE CLAIM that may come back empty. A waiter taken
// afterwards misses a Notify that landed in the gap, and the worker then
// sleeps out a full poll interval with work already queued — the exact stall
// this type exists to remove, reintroduced as a race that only shows up under
// load.
//
// The ordering argument, since it is the whole correctness case: if the load
// reads the channel Notify then replaces, that channel is closed and the
// waiter wakes. If instead it reads the REPLACEMENT, then Notify's store
// happened before the load, so the enqueue that prompted it happened before
// the claim this waiter is about to guard — and the claim finds the work
// itself. Either way no work waits on a timer.
//
// A nil WorkSignal returns a nil channel, which blocks forever in a select and
// leaves the caller polling.
func (s *WorkSignal) Waiter() <-chan struct{} {
	if s == nil {
		return nil
	}
	if c := s.ch.Load(); c != nil {
		return *c
	}
	// Zero-valued rather than built by NewWorkSignal. Rare and once.
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.ch.Load(); c != nil {
		return *c
	}
	c := make(chan struct{})
	s.ch.Store(&c)
	return c
}

// Notify wakes every worker currently waiting. Nil-safe, so a Service or
// worker wired without one simply polls.
//
// It is a broadcast rather than a hand-off to one worker because an enqueue is
// usually a fan-out of several ready steps, and waking one worker per step
// would drain them serially. The cost is that every IDLE worker issues a claim
// when only some will win. That is affordable because they were idle by
// definition, and a claim that finds nothing is a read — reads do not fsync.
// The regime to watch, if this ever needs revisiting, is many small runs
// against a large and mostly-idle worker pool, where claims scale with
// (submit rate x idle workers) rather than with the poll interval.
func (s *WorkSignal) Notify() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(chan struct{})
	old := s.ch.Swap(&next)
	if old != nil {
		close(*old)
	}
}
