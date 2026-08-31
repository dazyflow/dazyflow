// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// idempotency middleware test harness: a gateway with just the store, and
// a helper that drives one request through the wrapped handler.
func newIdemGateway() *HTTPGateway {
	return &HTTPGateway{idempotency: newIdempotencyStore()}
}

func doIdem(h *HTTPGateway, method, route, key string, handler func(http.ResponseWriter, *http.Request, core.Principal)) *httptest.ResponseRecorder {
	wrapped := h.idempotencyMiddleware(route, handler)
	req := httptest.NewRequest(method, "/x", nil)
	if key != "" {
		req.Header.Set(idempotencyHeader, key)
	}
	rw := httptest.NewRecorder()
	wrapped(rw, req, core.Principal{Subject: "alice"})
	return rw
}

// ok2xx is a handler that counts its invocations and returns 200 with a
// per-call body, so a replay (same body) is distinguishable from a
// re-execution (new body).
func countingHandler(calls *int32) func(http.ResponseWriter, *http.Request, core.Principal) {
	return func(rw http.ResponseWriter, _ *http.Request, _ core.Principal) {
		n := atomic.AddInt32(calls, 1)
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		fmt.Fprintf(rw, `{"call":%d}`, n)
	}
}

func TestIdempotency_ReplaysCachedResponse(t *testing.T) {
	h := newIdemGateway()
	var calls int32
	handler := countingHandler(&calls)

	first := doIdem(h, http.MethodPost, "/run", "k1", handler)
	if first.Code != 200 || first.Body.String() != `{"call":1}` {
		t.Fatalf("first call: code=%d body=%s", first.Code, first.Body.String())
	}
	if first.Header().Get("Idempotency-Replay") == "true" {
		t.Errorf("first call should not be flagged a replay")
	}

	second := doIdem(h, http.MethodPost, "/run", "k1", handler)
	if second.Code != 200 || second.Body.String() != `{"call":1}` {
		t.Fatalf("replay should return the first response verbatim: code=%d body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotency-Replay") != "true" {
		t.Errorf("replay should carry Idempotency-Replay: true")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("handler ran %d times, want 1 (the action must fire once)", got)
	}
}

func TestIdempotency_DistinctKeysAndRoutesDoNotCollide(t *testing.T) {
	h := newIdemGateway()
	var calls int32
	handler := countingHandler(&calls)

	doIdem(h, http.MethodPost, "/run", "k1", handler)
	doIdem(h, http.MethodPost, "/run", "k2", handler)    // different key
	doIdem(h, http.MethodPost, "/cancel", "k1", handler) // same key, different route
	doIdem(h, http.MethodPost, "/run", "", handler)      // no key → always runs
	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Errorf("handler ran %d times, want 4 (no false dedupe)", got)
	}
}

func TestIdempotency_NonSuccessIsNotCached(t *testing.T) {
	h := newIdemGateway()
	var calls int32
	handler := func(rw http.ResponseWriter, _ *http.Request, _ core.Principal) {
		atomic.AddInt32(&calls, 1)
		writeAPIError(rw, http.StatusInternalServerError, "boom", "transient")
	}
	doIdem(h, http.MethodPost, "/run", "k1", handler)
	doIdem(h, http.MethodPost, "/run", "k1", handler) // must retry, not be locked out
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("handler ran %d times, want 2 (a failed request must be retryable)", got)
	}
}

func TestIdempotency_NonMutatingMethodBypasses(t *testing.T) {
	h := newIdemGateway()
	var calls int32
	handler := countingHandler(&calls)
	doIdem(h, http.MethodGet, "/run", "k1", handler)
	doIdem(h, http.MethodGet, "/run", "k1", handler) // GET is not deduped
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("handler ran %d times, want 2 (GET should bypass the cache)", got)
	}
}

func TestIdempotency_KeyTooLong(t *testing.T) {
	h := newIdemGateway()
	var calls int32
	long := make([]byte, idempotencyKeyMax+1)
	for i := range long {
		long[i] = 'a'
	}
	rw := doIdem(h, http.MethodPost, "/run", string(long), countingHandler(&calls))
	if rw.Code != http.StatusBadRequest {
		t.Errorf("over-long key: code=%d, want 400", rw.Code)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Errorf("handler should not run for a rejected key")
	}
}

// TestIdempotency_ConcurrentSameKey_FiresOnce is the regression test for
// the get-then-put TOCTOU: many requests with the same key arriving while
// the first is still in flight must NOT all run the handler. Exactly one
// executes; the rest get 409 (in-flight). After it completes, a later
// retry replays the cached result.
func TestIdempotency_ConcurrentSameKey_FiresOnce(t *testing.T) {
	h := newIdemGateway()
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})

	// The winning request blocks inside the handler until released, so the
	// racers below are guaranteed to hit a held, in-flight reservation.
	blocking := func(rw http.ResponseWriter, _ *http.Request, _ core.Principal) {
		atomic.AddInt32(&calls, 1)
		close(started)
		<-release
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		fmt.Fprint(rw, `{"ok":true}`)
	}

	var winnerCode int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		winnerCode = doIdem(h, http.MethodPost, "/run", "race", blocking).Code
	}()

	<-started // the reservation is now held and the handler is blocked

	const racers = 16
	codes := make([]int, racers)
	var rwg sync.WaitGroup
	for i := 0; i < racers; i++ {
		rwg.Add(1)
		go func(i int) {
			defer rwg.Done()
			// These must not call the handler — a held reservation means 409.
			codes[i] = doIdem(h, http.MethodPost, "/run", "race", blocking).Code
		}(i)
	}
	rwg.Wait()

	for i, c := range codes {
		if c != http.StatusConflict {
			t.Errorf("racer %d got %d, want 409 (in-flight)", i, c)
		}
	}

	close(release)
	wg.Wait()
	if winnerCode != http.StatusOK {
		t.Errorf("winner got %d, want 200", winnerCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("handler ran %d times under concurrency, want exactly 1", got)
	}

	// Once settled, a later retry of the same key replays the cached 200.
	replay := doIdem(h, http.MethodPost, "/run", "race", blocking)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotency-Replay") != "true" {
		t.Errorf("post-settle retry: code=%d replay=%q, want 200 + replay", replay.Code, replay.Header().Get("Idempotency-Replay"))
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("handler ran %d times after replay, want still 1", got)
	}
}
