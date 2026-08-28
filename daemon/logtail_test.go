// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"testing"
)

// Lines split across multiple writes (and multiple lines in one write) must
// reassemble into whole lines in order.
func TestLogTailLineSplitting(t *testing.T) {
	lt := NewLogTail(10)
	lt.Write([]byte("alpha\nbra"))
	lt.Write([]byte("vo\ncharlie\n"))
	got := lt.Snapshot(0)
	want := []string{"alpha", "bravo", "charlie"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The ring keeps only the most recent `size` lines, oldest-first.
func TestLogTailRingEviction(t *testing.T) {
	lt := NewLogTail(3)
	for i := 0; i < 6; i++ {
		fmt.Fprintf(lt, "line%d\n", i)
	}
	got := lt.Snapshot(0)
	want := []string{"line3", "line4", "line5"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// Snapshot(max) trims to the last max.
	if g := lt.Snapshot(2); fmt.Sprint(g) != fmt.Sprint([]string{"line4", "line5"}) {
		t.Fatalf("Snapshot(2) = %v", g)
	}
}

// A subscriber receives lines written after it subscribed; cancel closes the
// channel and unregisters it.
func TestLogTailSubscribe(t *testing.T) {
	lt := NewLogTail(10)
	ch, cancel := lt.Subscribe()

	fmt.Fprintf(lt, "hello\n")
	if got := <-ch; got != "hello" {
		t.Fatalf("got %q, want hello", got)
	}

	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}
	// Cancel is idempotent and writes after cancel don't panic.
	cancel()
	fmt.Fprintf(lt, "after\n")
	if n := len(lt.subs); n != 0 {
		t.Fatalf("expected 0 subscribers after cancel, got %d", n)
	}
}
