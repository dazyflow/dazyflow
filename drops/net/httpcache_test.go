// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"net/http"
	"testing"
)

func memCacheStore() (map[string]string, func()) {
	m := map[string]string{}
	SetHTTPCacheStore(
		func(_ context.Context, tenant, name string) (string, error) { return m[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { m[tenant+"/"+name] = value; return nil },
	)
	return m, func() { SetHTTPCacheStore(nil, nil) }
}

func TestHTTPCacheRoundTrip(t *testing.T) {
	_, cleanup := memCacheStore()
	defer cleanup()
	if !httpCacheEnabled() {
		t.Fatal("cache should be enabled after wiring")
	}

	name := httpCacheName("g", "n", "k")
	writeCacheValidators(context.Background(), "t", name, cacheValidators{ETag: `"abc"`, LastModified: "Wed, 21 Oct 2026 07:28:00 GMT"})

	v := readCacheValidators(context.Background(), "t", name)
	if v == nil || v.ETag != `"abc"` {
		t.Fatalf("validators round-trip failed: %+v", v)
	}
}

func TestApplyConditionalHeaders(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	applyConditionalHeaders(req, &cacheValidators{ETag: `"e1"`, LastModified: "Wed, 21 Oct 2026 07:28:00 GMT"})
	if got := req.Header.Get("If-None-Match"); got != `"e1"` {
		t.Fatalf("If-None-Match = %q", got)
	}
	if got := req.Header.Get("If-Modified-Since"); got == "" {
		t.Fatal("If-Modified-Since not set")
	}

	// A caller-set validator must not be clobbered.
	req2, _ := http.NewRequest("GET", "https://example.com", nil)
	req2.Header.Set("If-None-Match", `"caller"`)
	applyConditionalHeaders(req2, &cacheValidators{ETag: `"stored"`})
	if got := req2.Header.Get("If-None-Match"); got != `"caller"` {
		t.Fatalf("caller's If-None-Match overwritten: %q", got)
	}

	// nil validators → no headers.
	req3, _ := http.NewRequest("GET", "https://example.com", nil)
	applyConditionalHeaders(req3, nil)
	if req3.Header.Get("If-None-Match") != "" || req3.Header.Get("If-Modified-Since") != "" {
		t.Fatal("nil validators should set no headers")
	}
}

func TestReadCacheNoStoreIsNil(t *testing.T) {
	SetHTTPCacheStore(nil, nil)
	if httpCacheEnabled() {
		t.Fatal("cache should be disabled")
	}
	if v := readCacheValidators(context.Background(), "t", "x"); v != nil {
		t.Fatalf("expected nil with no store, got %+v", v)
	}
}

func TestValidatorsFromResponse(t *testing.T) {
	h := http.Header{}
	h.Set("ETag", `"v9"`)
	h.Set("Last-Modified", "Wed, 21 Oct 2026 07:28:00 GMT")
	v := validatorsFromResponse(h)
	if v.ETag != `"v9"` || v.LastModified == "" {
		t.Fatalf("unexpected validators: %+v", v)
	}
}

// TestApplyConditionalHeaders_SentFlag covers the sent-validator signal used to
// gate the 304 fast-path: it's true only when a validator was actually
// attached, so an unsolicited 304 (nothing stored) isn't read as "not modified".
func TestApplyConditionalHeaders_SentFlag(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	if applyConditionalHeaders(req, nil) {
		t.Error("nil validators must report sent=false")
	}
	if applyConditionalHeaders(req, &cacheValidators{}) {
		t.Error("empty validators must report sent=false")
	}
	req2, _ := http.NewRequest("GET", "https://example.com", nil)
	if !applyConditionalHeaders(req2, &cacheValidators{ETag: `"e1"`}) {
		t.Error("a stored ETag must report sent=true")
	}
}

// TestClearCacheValidators verifies a stored validator is cleared (reads back
// nil) so a fresh 2xx that dropped its ETag stops us sending a stale one.
func TestClearCacheValidators(t *testing.T) {
	_, cleanup := memCacheStore()
	defer cleanup()
	name := httpCacheName("g", "n", "k")
	writeCacheValidators(context.Background(), "t", name, cacheValidators{ETag: `"abc"`})
	if v := readCacheValidators(context.Background(), "t", name); v == nil {
		t.Fatal("precondition: validator should be stored")
	}
	clearCacheValidators(context.Background(), "t", name)
	if v := readCacheValidators(context.Background(), "t", name); v != nil {
		t.Errorf("validator not cleared: %+v", v)
	}
}
