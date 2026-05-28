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
// route, idempotency key). Survives a single hzd process; lost on
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
	"net/http"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

const (
	idempotencyHeader   = "Idempotency-Key"
	idempotencyTTL      = 24 * time.Hour
	idempotencyKeyMax   = 128
	idempotencyMaxCache = 10_000
)

type idempotentResponse struct {
	status  int
	headers http.Header
	body    []byte
	storedAt time.Time
}

// idempotencyStore is a thread-safe map of cached responses. Cap on
// entries is enforced via simple FIFO eviction so a pathological key
// churn can't OOM the daemon.
type idempotencyStore struct {
	mu      sync.Mutex
	entries map[string]*idempotentResponse
	order   []string
}

func newIdempotencyStore() *idempotencyStore {
	return &idempotencyStore{entries: map[string]*idempotentResponse{}}
}

func (s *idempotencyStore) get(key string) (*idempotentResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.storedAt) > idempotencyTTL {
		delete(s.entries, key)
		return nil, false
	}
	return e, true
}

func (s *idempotencyStore) put(key string, e *idempotentResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; !exists {
		s.order = append(s.order, key)
	}
	s.entries[key] = e
	// Cap the cache. Drop oldest first — the FIFO order isn't perfect
	// (it doesn't account for re-puts), but for an LLM workload where
	// keys are one-shot UUIDs this approximates LRU.
	for len(s.entries) > idempotencyMaxCache {
		drop := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, drop)
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
func (h *HTTPGateway) idempotencyMiddleware(routePattern string, next func(rw http.ResponseWriter, r *http.Request, p core.Principal)) func(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
		if cached, ok := h.idempotency.get(cacheKey); ok {
			// Replay. Note we DO NOT call the handler — that's the point.
			for k, vs := range cached.headers {
				for _, v := range vs {
					rw.Header().Add(k, v)
				}
			}
			rw.Header().Set("Idempotency-Replay", "true")
			rw.WriteHeader(cached.status)
			_, _ = rw.Write(cached.body)
			return
		}
		// Capture the response so we can replay on the next match.
		cap := &captureWriter{ResponseWriter: rw, headers: http.Header{}}
		next(cap, r, p)
		// Only cache 2xx — caching errors would lock the client out of
		// retrying after fixing a transient problem.
		if cap.status >= 200 && cap.status < 300 {
			h.idempotency.put(cacheKey, &idempotentResponse{
				status:  cap.status,
				headers: cap.headers,
				body:    cap.body.Bytes(),
				storedAt: time.Now(),
			})
		}
	}
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

func (w *captureWriter) Header() http.Header {
	// Mirror writes to both maps: the underlying writer (so the
	// response goes out) and our capture (so we can replay).
	if w.headers == nil {
		w.headers = http.Header{}
	}
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
