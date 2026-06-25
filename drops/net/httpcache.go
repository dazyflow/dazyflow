package net

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

// Conditional-request caching for http_request (opt-in via the `cache_key`
// param). A poll that re-fetches an unchanged resource is the dominant wasted
// cost on a shared polling fleet; sending the validators the server last gave
// us (ETag → If-None-Match, Last-Modified → If-Modified-Since) lets it answer
// 304 Not Modified with no body, costing ~0 against its rate budget. The drop
// turns a 304 into an explicit "not modified" result so a downstream Branch
// can skip work, and reports the empty fire to the scheduler (adaptive
// backoff). The validators are persisted per (tenant, flow, node, cache_key)
// in the same encrypted secret store as cursors, under the reserved
// "httpcache." prefix; when unwired (tests / in-process) caching is simply
// off and every request is unconditional.

// CacheReader returns the stored validators for an exact tenant/name, or
// ("", nil) when nothing is stored. CacheWriter persists them. Shapes match
// the cursor store so the daemon backs them with the same store.
type (
	CacheReader func(ctx context.Context, tenant, name string) (string, error)
	CacheWriter func(ctx context.Context, tenant, name, value string) error
)

var (
	cacheMu     sync.RWMutex
	cacheReader CacheReader
	cacheWriter CacheWriter
)

// SetHTTPCacheStore wires the validator persistence. cmd/dzd points it at the
// encrypted secret store under the "httpcache." prefix.
func SetHTTPCacheStore(r CacheReader, w CacheWriter) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cacheReader, cacheWriter = r, w
}

// httpCacheEnabled reports whether a backing store is wired.
func httpCacheEnabled() bool {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return cacheReader != nil && cacheWriter != nil
}

// cacheValidators are what we persist between conditional fetches.
type cacheValidators struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// httpCacheName is the tenant-scoped secret name a node's validators live
// under. Keyed by (flow, node, cache_key) so two http_request nodes — or one
// node fetching several URLs under distinct keys — don't share validators.
func httpCacheName(graphID, nodeID, cacheKey string) string {
	return "httpcache." + graphID + "." + nodeID + "." + cacheKey
}

// readCacheValidators loads a node's stored validators, or nil when caching is
// off, nothing is stored, or the value is unparseable.
func readCacheValidators(ctx context.Context, tenant, name string) *cacheValidators {
	cacheMu.RLock()
	r := cacheReader
	cacheMu.RUnlock()
	if r == nil || tenant == "" {
		return nil
	}
	raw, err := r(ctx, tenant, name)
	if err != nil || raw == "" {
		return nil
	}
	var v cacheValidators
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	if v.ETag == "" && v.LastModified == "" {
		return nil
	}
	return &v
}

// writeCacheValidators persists a response's validators. Best-effort: a failed
// write just means the next fetch is unconditional (re-downloads once).
func writeCacheValidators(ctx context.Context, tenant, name string, v cacheValidators) {
	cacheMu.RLock()
	w := cacheWriter
	cacheMu.RUnlock()
	if w == nil || tenant == "" || (v.ETag == "" && v.LastModified == "") {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_ = w(ctx, tenant, name, string(b))
}

// applyConditionalHeaders sets If-None-Match / If-Modified-Since from stored
// validators (without clobbering ones the caller already set), so the server
// can short-circuit with a 304. No-op when nothing is stored.
func applyConditionalHeaders(req *http.Request, v *cacheValidators) {
	if v == nil {
		return
	}
	if v.ETag != "" && req.Header.Get("If-None-Match") == "" {
		req.Header.Set("If-None-Match", v.ETag)
	}
	if v.LastModified != "" && req.Header.Get("If-Modified-Since") == "" {
		req.Header.Set("If-Modified-Since", v.LastModified)
	}
}

// validatorsFromResponse extracts the ETag / Last-Modified a 2xx response
// carries so the next request can be made conditional.
func validatorsFromResponse(h http.Header) cacheValidators {
	return cacheValidators{
		ETag:         h.Get("ETag"),
		LastModified: h.Get("Last-Modified"),
	}
}
