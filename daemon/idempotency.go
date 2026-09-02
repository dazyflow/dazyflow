// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// idempotency.go implements the Idempotency-Key contract from the
// OpenAPI spec: a mutating request that carries the same key within
// the cache window returns the original response verbatim.
//
// Critical for LLM clients: an agent that retries a /run or /cancel
// (because the network blip swallowed its first response) must NOT
// fire the action twice. The standard fix is for the client to mint
// a UUID, send it as `Idempotency-Key`, and trust the server to
// dedupe.
//
// Scope today: 24h TTL, in-memory map keyed by (principal subject,
// route, idempotency key). Survives a single dzd process; lost on
// restart. The spec doesn't require persistence — Stripe and others
// document the same "best-effort within a window" semantics.
//
// Scope NOT today: persistence (Postgres-backed cache), key
// expiration via janitor goroutine (we mark stale-on-read instead so
// we don't need a background sweeper), per-key request-shape hashing
// (would catch "same key, different body" misuse — useful but the
// spec is silent on it).

package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// idempotencyAPI serves the idempotency-key endpoints. Its fields are the whole of what
// those handlers touch.
type idempotencyAPI struct {
	idempotency *idempotencyStore
}

// idempotencyAPI builds them from the gateway's configuration.
func (h *HTTPGateway) idempotencyAPI() *idempotencyAPI {
	return &idempotencyAPI{idempotency: h.idempotency}
}

const (
	idempotencyHeader   = "Idempotency-Key"
	idempotencyTTL      = 24 * time.Hour
	idempotencyKeyMax   = 128
	idempotencyMaxCache = 10_000
)

type idempotentResponse struct {
	status   int
	headers  http.Header
	body     []byte
	storedAt time.Time
	// reqHash binds the entry to the request that minted it: a sha256 over
	// method+path+body. A later request reusing the same key but carrying a
	// different payload is misuse (the spec's "same key, different body"),
	// and replaying the first response would silently mask it — so a hash
	// mismatch is rejected with 422 instead of replayed.
	reqHash string
	// done is false while the request that reserved this key is still
	// in flight (the handler hasn't returned yet), and true once a
	// completed response has been cached. An in-flight entry exists so
	// a concurrent retry of the SAME key cannot also run the handler —
	// closing the get-then-put TOCTOU that let an action fire twice.
	done bool
}

// idempotencyStore is a thread-safe map of cached responses. Cap on
// entries is enforced via simple FIFO eviction so a pathological key
// churn can't OOM the daemon. Invariant: a key is in `entries` iff it
// is in `order`, exactly once — begin/commit/abort/evict all preserve it.
type idempotencyStore struct {
	mu      sync.Mutex
	entries map[string]*idempotentResponse
	order   []string
}

func newIdempotencyStore() *idempotencyStore {
	return &idempotencyStore{entries: map[string]*idempotentResponse{}}
}

// begin atomically claims a key for the calling request. It returns
// either the existing entry (fresh=false) — which the caller inspects:
// a `done` entry is replayed, a not-`done` entry means another request
// holds the key right now (respond 409) — or, when the key is free,
// reserves an in-flight marker and returns fresh=true, making the caller
// the sole owner responsible for resolving it via commit or abort.
//
// A stale entry (older than the TTL) is reclaimed as if absent: a stuck
// in-flight reservation (e.g. a handler that hung and never resolved)
// can't lock a key out forever. The common crash/panic case is handled
// up front by the caller's deferred abort; this is the backstop.
func (s *idempotencyStore) begin(key, reqHash string) (existing *idempotentResponse, fresh bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		if time.Since(e.storedAt) <= idempotencyTTL {
			return e, false
		}
		// Stale — drop it and fall through to a fresh reservation.
		delete(s.entries, key)
		s.removeFromOrderLocked(key)
	}
	// done=false: reserved. reqHash is stamped now so a concurrent retry of
	// the SAME key with a DIFFERENT body is caught while the first request
	// is still in flight, not only after it commits.
	s.entries[key] = &idempotentResponse{storedAt: time.Now(), reqHash: reqHash}
	s.order = append(s.order, key)
	s.evictLocked()
	return nil, true
}

// commit replaces this key's in-flight reservation with the completed
// response. Called once, by the owner, on a cacheable (2xx) result.
func (s *idempotencyStore) commit(key string, e *idempotentResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.done = true
	// Carry the request hash from the reservation onto the completed entry
	// so a later replay can still detect a same-key/different-body misuse.
	if prev, ok := s.entries[key]; ok {
		if e.reqHash == "" {
			e.reqHash = prev.reqHash
		}
	} else {
		// Reservation was evicted under cache pressure; re-add it.
		s.order = append(s.order, key)
	}
	s.entries[key] = e
	s.evictLocked()
}

// abort releases a reservation without caching a response, so the client
// is free to retry. Called by the owner when the handler returned a
// non-2xx (a transient failure the client may fix and retry) or panicked.
func (s *idempotencyStore) abort(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && !e.done {
		delete(s.entries, key)
		s.removeFromOrderLocked(key)
	}
}

// evictLocked caps the cache, dropping oldest-first. Caller holds s.mu.
func (s *idempotencyStore) evictLocked() {
	for len(s.entries) > idempotencyMaxCache {
		drop := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, drop)
	}
}

// removeFromOrderLocked deletes the first occurrence of key from order.
// Caller holds s.mu. O(n), but only on the rare abort/stale-reclaim path.
func (s *idempotencyStore) removeFromOrderLocked(key string) {
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			return
		}
	}
}

// idempotencyMiddleware wraps a handler with the idempotency contract.
// Only applies to POST/PATCH (PUT is full-document replace, so a retry
// is naturally idempotent at the data level — the cache adds little).
//
// Key is composed of (principal subject, method, route pattern,
// header value). The route pattern is included so the same key on
// /flows/{id}/run and /flows/{id}/cancel doesn't collide.
//
// Cached responses replay status + body + Content-Type. Other headers
// (like Set-Cookie or Location) are not replayed today — we don't
// emit them from the idempotent routes. If that changes, expand the
// `replayable` allowlist in the response capture below.
func (h *idempotencyAPI) idempotencyMiddleware(routePattern string, next func(rw http.ResponseWriter, r *http.Request, p core.Principal)) func(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	return func(rw http.ResponseWriter, r *http.Request, p core.Principal) {
		if r.Method != http.MethodPost && r.Method != http.MethodPatch {
			next(rw, r, p)
			return
		}
		key := r.Header.Get(idempotencyHeader)
		if key == "" {
			next(rw, r, p)
			return
		}
		if len(key) > idempotencyKeyMax {
			writeAPIError(rw, http.StatusBadRequest, "idempotency_key_too_long",
				"Idempotency-Key must be <= 128 chars")
			return
		}
		cacheKey := p.Subject + "|" + r.Method + "|" + routePattern + "|" + key
		// Buffer the body so we can hash it AND hand an intact copy to the
		// downstream handler. The body hash binds the cached entry to the
		// exact payload that minted it (plus method+path), so reusing a key
		// with a different payload can't silently replay the first response.
		reqHash, err := readBodyHash(r)
		if err != nil {
			writeAPIError(rw, http.StatusBadRequest, "bad_request", "could not read request body")
			return
		}
		existing, fresh := h.idempotency.begin(cacheKey, reqHash)
		if !fresh {
			// Same key, different request shape is misuse — refuse rather
			// than replay a response for a payload the client didn't send.
			if existing.reqHash != "" && existing.reqHash != reqHash {
				rw.Header().Set("Idempotency-Replay", "false")
				writeAPIError(rw, http.StatusUnprocessableEntity, "idempotency_key_reused",
					"this Idempotency-Key was already used with a different request body")
				return
			}
			if !existing.done {
				// A request with this key is in flight right now. Running
				// the handler again would fire the action twice, so refuse
				// — the client retries and gets the cached result once the
				// first request finishes. (Stripe's documented semantics.)
				rw.Header().Set("Idempotency-Replay", "false")
				writeAPIError(rw, http.StatusConflict, "idempotency_key_in_flight",
					"a request with this Idempotency-Key is already being processed; retry shortly")
				return
			}
			// Replay the cached response. We DO NOT call the handler.
			for k, vs := range existing.headers {
				for _, v := range vs {
					rw.Header().Add(k, v)
				}
			}
			rw.Header().Set("Idempotency-Replay", "true")
			rw.WriteHeader(existing.status)
			_, _ = rw.Write(existing.body)
			return
		}
		// We own the reservation. Resolve it no matter what: abort on a
		// panic or a non-2xx so the key isn't left locked or caching a
		// failure; commit only a cacheable result. The defer is the
		// backstop for a panicking handler (the http stack recovers it).
		committed := false
		defer func() {
			if !committed {
				h.idempotency.abort(cacheKey)
			}
		}()
		cw := &captureWriter{ResponseWriter: rw, headers: http.Header{}}
		next(cw, r, p)
		// Only cache 2xx — caching errors would lock the client out of
		// retrying after fixing a transient problem.
		if cw.status >= 200 && cw.status < 300 {
			h.idempotency.commit(cacheKey, &idempotentResponse{
				status:   cw.status,
				headers:  cw.headers,
				body:     cw.body.Bytes(),
				storedAt: time.Now(),
			})
			committed = true
		}
	}
}

// readBodyHash reads r.Body fully, restores it (so the downstream handler
// reads the same bytes), and returns a sha256 over method+path+body. The
// method+path are folded in so the same body on two different routes hashes
// differently — defense in depth atop the route-pattern cache key. A nil
// body hashes as the empty body.
func readBodyHash(r *http.Request) (string, error) {
	var raw []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return "", err
		}
		raw = b
		// Restore the consumed body for the downstream handler.
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	sum := sha256.New()
	sum.Write([]byte(r.Method))
	sum.Write([]byte("\n"))
	sum.Write([]byte(r.URL.Path))
	sum.Write([]byte("\n"))
	sum.Write(raw)
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// captureWriter buffers the response so it can be cached AND emitted
// to the real ResponseWriter. We replay Content-Type at minimum — the
// rest of the headers are mirrored for fidelity.
type captureWriter struct {
	http.ResponseWriter
	headers     http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

// Header delegates to the real writer; the capture snapshot is taken in
// WriteHeader, when net/http freezes the headers.
func (w *captureWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *captureWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = code
	// Snapshot the real writer's headers at the moment WriteHeader is
	// called — that's when net/http freezes them.
	w.headers = w.ResponseWriter.Header().Clone()
	w.ResponseWriter.WriteHeader(code)
}

func (w *captureWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}
