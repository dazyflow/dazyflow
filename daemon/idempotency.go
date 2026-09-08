// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// The Idempotency-Key contract: a mutating request carrying the same key within
// the window returns the original response verbatim, so an agent that retries a
// /run because a blip swallowed the first response does not fire it twice.
//
// In-memory, keyed by (subject, route, key), lost on restart — the same
// best-effort-within-a-window semantics Stripe documents. Entries are marked
// stale on read rather than swept, so there is no janitor goroutine.

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

type idempotencyAPI struct {
	idempotency *idempotencyStore
}

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
	reqHash  string
	done     bool
	runID    string
}

type idempotencyStore struct {
	mu      sync.Mutex
	entries map[string]*idempotentResponse
	order   []string
}

func newIdempotencyStore() *idempotencyStore {
	return &idempotencyStore{entries: map[string]*idempotentResponse{}}
}

func (s *idempotencyStore) begin(key, reqHash string) (existing *idempotentResponse, fresh bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok {
		if time.Since(e.storedAt) <= idempotencyTTL {
			return e, false
		}
		delete(s.entries, key)
		s.removeFromOrderLocked(key)
	}
	s.entries[key] = &idempotentResponse{storedAt: time.Now(), reqHash: reqHash}
	s.order = append(s.order, key)
	s.evictLocked()
	return nil, true
}

func (s *idempotencyStore) commit(key string, e *idempotentResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.done = true
	if prev, ok := s.entries[key]; ok {
		if e.reqHash == "" {
			e.reqHash = prev.reqHash
		}
		if e.runID == "" {
			e.runID = prev.runID
		}
	} else {
		s.order = append(s.order, key)
	}
	s.entries[key] = e
	s.evictLocked()
}

func (s *idempotencyStore) attach(key, runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && !e.done {
		e.runID = runID
	}
}

func (s *idempotencyStore) abort(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[key]; ok && !e.done {
		delete(s.entries, key)
		s.removeFromOrderLocked(key)
	}
}

func (s *idempotencyStore) evictLocked() {
	for len(s.entries) > idempotencyMaxCache {
		drop := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, drop)
	}
}

func (s *idempotencyStore) removeFromOrderLocked(key string) {
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			return
		}
	}
}

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
		reqHash, err := readBodyHash(r)
		if err != nil {
			writeAPIError(rw, http.StatusBadRequest, "bad_request", "could not read request body")
			return
		}
		existing, fresh := h.idempotency.begin(cacheKey, reqHash)
		if !fresh {
			if existing.reqHash != "" && existing.reqHash != reqHash {
				rw.Header().Set("Idempotency-Replay", "false")
				writeAPIError(rw, http.StatusUnprocessableEntity, "idempotency_key_reused",
					"this Idempotency-Key was already used with a different request body")
				return
			}
			if !existing.done {
				rw.Header().Set("Idempotency-Replay", "false")
				writeAPIError(rw, http.StatusConflict, "idempotency_key_in_flight",
					"a request with this Idempotency-Key is already being processed; retry shortly")
				return
			}
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
		committed := false
		defer func() {
			if !committed {
				h.idempotency.abort(cacheKey)
			}
		}()
		cw := &captureWriter{ResponseWriter: rw, headers: http.Header{}}
		next(cw, r, p)
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

func readBodyHash(r *http.Request) (string, error) {
	var raw []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return "", err
		}
		raw = b
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

type captureWriter struct {
	http.ResponseWriter
	headers     http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func (w *captureWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *captureWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = code
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
