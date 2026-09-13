// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net"
	"testing"
	"time"
)

// harness is a listener plus the accept loop a real server runs against it.
// One loop, not an Accept per assertion: a pending Accept has ALREADY taken a
// slot (the semaphore is claimed before the syscall), so spawning one per check
// leaves a goroutine waiting to swallow the next slot that frees up — which is
// a property of the design, and was enough to make a naive test lie.
type harness struct {
	ln      net.Listener
	accepts chan net.Conn
	t       *testing.T
}

func newHarness(t *testing.T, maxConns int) *harness {
	t.Helper()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	h := &harness{ln: newLimitListener(raw, maxConns), accepts: make(chan net.Conn, 16), t: t}
	go func() {
		for {
			c, aerr := h.ln.Accept()
			if aerr != nil {
				return
			}
			h.accepts <- c
		}
	}()
	return h
}

func (h *harness) dial() {
	h.t.Helper()
	c, err := net.Dial("tcp", h.ln.Addr().String())
	if err != nil {
		h.t.Fatalf("dial: %v", err)
	}
	h.t.Cleanup(func() { _ = c.Close() })
}

// arrived reports whether the accept loop got another connection promptly. Past
// the cap Accept blocks rather than erroring, so "nothing arrived" is the
// assertion — there is no error to inspect.
func (h *harness) arrived() (net.Conn, bool) {
	h.t.Helper()
	select {
	case c := <-h.accepts:
		return c, true
	case <-time.After(300 * time.Millisecond):
		return nil, false
	}
}

func TestLimitListener_StopsAcceptingAtTheCap(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 2)
	h.dial()
	h.dial()
	h.dial()

	for i := range 2 {
		if _, ok := h.arrived(); !ok {
			t.Fatalf("connection %d should have been accepted", i+1)
		}
	}
	if _, ok := h.arrived(); ok {
		t.Error("a third connection was accepted past a cap of two")
	}
}

// The slot has to come back when a connection closes, or the ceiling ratchets
// down to nothing over the life of the process.
func TestLimitListener_ReleasesWhenAConnectionCloses(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 1)
	h.dial()
	first, ok := h.arrived()
	if !ok {
		t.Fatal("the first connection was not accepted")
	}

	h.dial()
	if _, ok := h.arrived(); ok {
		t.Fatal("accepted a second connection while the first was still open")
	}

	_ = first.Close()
	if _, ok := h.arrived(); !ok {
		t.Error("closing the first connection did not free its slot")
	}
}

// A net.Conn may be closed twice — by a handler's defer and by the server's own
// teardown. Releasing twice would hand back a slot nobody holds, and the cap
// would drift open for the rest of the process's life.
func TestLimitListener_ClosingTwiceReleasesOneSlot(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 1)
	h.dial()
	first, ok := h.arrived()
	if !ok {
		t.Fatal("the first connection was not accepted")
	}

	_ = first.Close()
	_ = first.Close()

	h.dial()
	h.dial()
	if _, ok := h.arrived(); !ok {
		t.Fatal("the freed slot was not reusable")
	}
	if _, ok := h.arrived(); ok {
		t.Error("a double close handed back two slots — the cap now drifts open")
	}
}

func TestLimitListener_ZeroOrNegativeLeavesTheListenerAlone(t *testing.T) {
	t.Parallel()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer raw.Close()

	if got := newLimitListener(raw, 0); got != raw {
		t.Error("a cap of zero should not wrap the listener")
	}
	if got := newLimitListener(raw, -1); got != raw {
		t.Error("a negative cap should not wrap the listener")
	}
}
