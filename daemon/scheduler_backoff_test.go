// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/pollstate"
)

func TestEffectiveInterval_BackoffCurve(t *testing.T) {
	base := 300 * time.Second
	cases := []struct {
		streak int
		want   time.Duration
	}{
		{0, base},
		{1, base}, // grace
		{2, base}, // grace
		{3, 2 * base},
		{4, 4 * base},
		{5, 8 * base},
		{6, 8 * base}, // capped at maxPollBackoffMultiplier
		{99, 8 * base},
	}
	for _, c := range cases {
		e := &scheduledGraph{interval: base, emptyStreak: c.streak}
		if got := e.effectiveInterval(); got != c.want {
			t.Errorf("streak %d: effectiveInterval = %v, want %v", c.streak, got, c.want)
		}
	}
}

func TestEffectiveInterval_NonPollReturnsBase(t *testing.T) {
	e := &scheduledGraph{scheduleFn: nil, interval: 0, emptyStreak: 5}
	if got := e.effectiveInterval(); got != 0 {
		t.Fatalf("non-poll entry: got %v, want 0", got)
	}
}

func TestEffectiveInterval_CeilingClamp(t *testing.T) {
	// A near-ceiling base widened 8× must clamp to the poll ceiling.
	base := time.Duration(core.MaxPollIntervalSeconds/2) * time.Second
	e := &scheduledGraph{interval: base, emptyStreak: 9}
	ceiling := time.Duration(core.MaxPollIntervalSeconds) * time.Second
	if got := e.effectiveInterval(); got > ceiling {
		t.Fatalf("effectiveInterval %v exceeds ceiling %v", got, ceiling)
	}
}

func TestPollJitter_DeterministicAndBounded(t *testing.T) {
	interval := 300 * time.Second
	a1 := pollJitter("t/ws/g@n", interval)
	a2 := pollJitter("t/ws/g@n", interval)
	if a1 != a2 {
		t.Fatalf("jitter not deterministic: %v vs %v", a1, a2)
	}
	if a1 < 0 || a1 >= interval/4 {
		t.Fatalf("jitter %v out of [0, interval/4=%v)", a1, interval/4)
	}
	// Different keys should (very likely) land on different offsets.
	if pollJitter("a", interval) == pollJitter("b", interval) &&
		pollJitter("c", interval) == pollJitter("d", interval) {
		t.Fatal("jitter shows no spread across distinct keys")
	}
}

func TestPollJitter_CappedByMax(t *testing.T) {
	// A daily poll: interval/4 = 6h, but the absolute cap is maxPollJitter.
	day := 24 * time.Hour
	for _, key := range []string{"a", "b", "c", "d", "e"} {
		if j := pollJitter(key, day); j >= maxPollJitter {
			t.Fatalf("jitter %v exceeds cap %v", j, maxPollJitter)
		}
	}
}

func TestRefreshEmptyStreak(t *testing.T) {
	t0 := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return t0.Add(d).Format(time.RFC3339) }

	var marker *pollstate.Marker
	s := &Scheduler{pollState: func(_ context.Context, _, _ string) *pollstate.Marker { return marker }}
	e := &scheduledGraph{tenant: "t", graphID: "g", interval: time.Minute}

	// Three fresh empty markers → streak climbs to 3.
	for i := 1; i <= 3; i++ {
		marker = &pollstate.Marker{Empty: true, At: at(time.Duration(i) * time.Second)}
		s.foldPollOutcomeLocked(e, s.readPollMarker(context.Background(), e))
		if e.emptyStreak != i {
			t.Fatalf("after %d empty fires, streak = %d", i, e.emptyStreak)
		}
	}

	// Re-reading the SAME (stale) marker must not inflate the streak.
	s.foldPollOutcomeLocked(e, s.readPollMarker(context.Background(), e))
	if e.emptyStreak != 3 {
		t.Fatalf("stale marker inflated streak to %d", e.emptyStreak)
	}

	// A fresh ACTIVE marker resets the streak.
	marker = &pollstate.Marker{Empty: false, At: at(10 * time.Second)}
	s.foldPollOutcomeLocked(e, s.readPollMarker(context.Background(), e))
	if e.emptyStreak != 0 {
		t.Fatalf("active marker did not reset streak (got %d)", e.emptyStreak)
	}
}

func TestRefreshEmptyStreak_NoReaderNoop(t *testing.T) {
	s := &Scheduler{} // pollState nil
	e := &scheduledGraph{tenant: "t", graphID: "g", interval: time.Minute, emptyStreak: 2}
	s.foldPollOutcomeLocked(e, s.readPollMarker(context.Background(), e))
	if e.emptyStreak != 2 {
		t.Fatalf("nil reader should be a no-op, streak changed to %d", e.emptyStreak)
	}
}

// The poll-outcome marker read is a store round-trip (Postgres-backed in
// production). fireDue must not hold s.mu across it, or a slow — or hung —
// store stalls rescan's map swap, reanchor, and TrackedCount behind an
// unrelated database call.
//
// The reader below blocks until the test observes that the scheduler's mutex is
// still free. If fireDue ever takes the lock before reading again, TrackedCount
// blocks, the signal never arrives, and this deadlocks into a timeout.
func TestScheduler_PollMarkerReadDoesNotHoldLock(t *testing.T) {
	// An empty workspace map makes fireGraph fail its Open and return quietly,
	// so the test exercises fireDue's locking without needing a real flow.
	s := NewScheduler(&Service{Workspaces: MapWorkspaces{}})

	lockFree := make(chan struct{})
	release := make(chan struct{})
	s.SetPollStateReader(func(context.Context, string, string) *pollstate.Marker {
		// Prove the lock is available WHILE the reader is in flight.
		go func() {
			s.TrackedCount() // would block if fireDue held s.mu
			close(lockFree)
		}()
		<-release
		return nil
	})

	e := &scheduledGraph{
		graphID: "g", tenant: "t", workspace: "ws",
		interval:   time.Minute,
		scheduleAt: time.Now().Add(-time.Second), // due
	}
	s.tracked["t/ws/g@n"] = e

	done := make(chan struct{})
	go func() { s.fireDue(context.Background()); close(done) }()

	select {
	case <-lockFree:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("scheduler mutex was held across the poll-marker read")
	}
	close(release)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("fireDue did not return")
	}
	if !e.scheduleAt.After(time.Now()) {
		t.Error("next fire should have been advanced past now")
	}
}
