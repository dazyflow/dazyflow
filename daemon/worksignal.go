// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"sync"
	"sync/atomic"
)

// Wakes idle workers the moment work is enqueued, so a submit starts now rather
// than on the next poll tick.
type WorkSignal struct {
	// On the claim loop's hot path, read once per iteration by every worker.
	ch atomic.Pointer[chan struct{}]
	mu sync.Mutex
}

func NewWorkSignal() *WorkSignal {
	s := &WorkSignal{}
	c := make(chan struct{})
	s.ch.Store(&c)
	return s
}

// The channel must be taken BEFORE the claim attempt: a waiter registered after
// an empty claim misses a wake that landed in between, and then sleeps the full
// interval.
func (s *WorkSignal) Waiter() <-chan struct{} {
	if s == nil {
		return nil
	}
	if c := s.ch.Load(); c != nil {
		return *c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.ch.Load(); c != nil {
		return *c
	}
	c := make(chan struct{})
	s.ch.Store(&c)
	return c
}

// Nil-safe, so a Service without one wired still runs.
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
