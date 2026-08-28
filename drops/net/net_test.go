// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ===== do.go Do =======================================================

func TestCov_DoGETSucceeds(t *testing.T) {
	var gotHdr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr = r.Header.Get("X-Test")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("body-bytes"))
	}))
	defer srv.Close()

	status, body, hdr, err := Do(context.Background(), "GET", srv.URL,
		map[string]string{"X-Test": "v"}, nil, 5000, 1<<20)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != 200 || string(body) != "body-bytes" {
		t.Errorf("status=%d body=%q", status, body)
	}
	if hdr == nil {
		t.Error("nil header")
	}
	if gotHdr != "v" {
		t.Errorf("server saw X-Test=%q", gotHdr)
	}
}

func TestCov_DoPOSTWithBody(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
		w.WriteHeader(201)
	}))
	defer srv.Close()

	status, _, _, err := Do(context.Background(), "POST", srv.URL, nil, []byte("payload"), 0, 0)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != 201 || seen != "payload" {
		t.Errorf("status=%d seen=%q", status, seen)
	}
}

func TestCov_DoBodyExceedsCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 2048))
	}))
	defer srv.Close()

	_, _, _, err := Do(context.Background(), "GET", srv.URL, nil, nil, 5000, 100)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected 'exceeds' cap error, got %v", err)
	}
}

func TestCov_DoBadRequestError(t *testing.T) {
	// A malformed method makes http.NewRequestWithContext fail.
	_, _, _, err := Do(context.Background(), "BAD METHOD", "http://x.example", nil, nil, 1000, 1000)
	if err == nil {
		t.Fatal("expected request-build error")
	}
}

func TestCov_DoEgressBlocked(t *testing.T) {
	if err := SetEgressAllowlist([]string{"allowed.example.com"}); err != nil {
		t.Fatalf("set allowlist: %v", err)
	}
	defer func() { _ = SetEgressAllowlist(nil) }()

	_, _, _, err := Do(context.Background(), "GET", "https://blocked.example.com/", nil, nil, 1000, 1000)
	if err == nil || !strings.Contains(err.Error(), "egress_blocked") {
		t.Fatalf("expected egress_blocked, got %v", err)
	}
}

// ===== http_request.go buildRequestBody ===============================

func TestCov_BuildRequestBody(t *testing.T) {
	// string input.
	r, err := buildRequestBody(core.Job{Input: map[string]core.Ref{"request_body": {Inline: "hi"}}})
	if err != nil || r == nil {
		t.Fatalf("string: r=%v err=%v", r, err)
	}
	b, _ := io.ReadAll(r)
	if string(b) != "hi" {
		t.Errorf("string body = %q", b)
	}

	// []byte input.
	r2, _ := buildRequestBody(core.Job{Input: map[string]core.Ref{"request_body": {Inline: []byte("bytes")}}})
	b2, _ := io.ReadAll(r2)
	if string(b2) != "bytes" {
		t.Errorf("bytes body = %q", b2)
	}

	// struct/map input → JSON marshalled.
	r3, _ := buildRequestBody(core.Job{Input: map[string]core.Ref{"request_body": {Inline: map[string]any{"a": 1}}}})
	b3, _ := io.ReadAll(r3)
	if !strings.Contains(string(b3), `"a":1`) {
		t.Errorf("map body = %q", b3)
	}

	// nil input → falls through to params.body.
	r4, _ := buildRequestBody(core.Job{
		Input:  map[string]core.Ref{"request_body": {Inline: nil}},
		Params: map[string]any{"body": "fromparam"},
	})
	b4, _ := io.ReadAll(r4)
	if string(b4) != "fromparam" {
		t.Errorf("param body = %q", b4)
	}

	// no input, no param → nil reader.
	r5, _ := buildRequestBody(core.Job{})
	if r5 != nil {
		t.Error("expected nil reader when no body")
	}
}

// ===== http_request.go statusAccepted / formatExpectStatus ===========

func TestCov_StatusAccepted(t *testing.T) {
	if !statusAccepted(204, nil) {
		t.Error("204 should be accepted with no expect list")
	}
	if statusAccepted(404, nil) {
		t.Error("404 should not be accepted by default")
	}
	if !statusAccepted(404, []int{404, 410}) {
		t.Error("404 should be accepted when listed")
	}
	if statusAccepted(500, []int{404}) {
		t.Error("500 not in list")
	}
}

func TestCov_FormatExpectStatus(t *testing.T) {
	if got := formatExpectStatus(nil); got != "2xx" {
		t.Errorf("got %q, want 2xx", got)
	}
	if got := formatExpectStatus([]int{200, 201, 404}); got != "200,201,404" {
		t.Errorf("got %q", got)
	}
}

// ===== http_request.go SSRF surface ===================================

func TestCov_IsSSRFError(t *testing.T) {
	if !IsSSRFError(errors.New("dial tcp: ssrf_blocked: bad")) {
		t.Error("should detect ssrf_blocked")
	}
	if IsSSRFError(errors.New("some other error")) {
		t.Error("should not match unrelated error")
	}
	if IsSSRFError(nil) {
		t.Error("nil is not an SSRF error")
	}
}

func TestCov_SafeHTTPClientCached(t *testing.T) {
	c1 := SafeHTTPClient(5*time.Second, false)
	c2 := SafeHTTPClient(5*time.Second, false)
	if c1 != c2 {
		t.Error("same (timeout, allowPrivate) should return cached client")
	}
	c3 := SafeHTTPClient(9*time.Second, false)
	if c1 == c3 {
		t.Error("different timeout should build a distinct client")
	}
	if c1.Timeout != 5*time.Second {
		t.Errorf("timeout = %v", c1.Timeout)
	}
}

func TestCov_SSRFGuardAndDialControl(t *testing.T) {
	// With private egress disabled, the dial control + CheckDialHost block
	// loopback/private. TestMain enables it globally, so flip it off here.
	SetAllowPrivateEgress(false)
	defer SetAllowPrivateEgress(true)

	ctrl := SSRFDialControl()
	if ctrl == nil {
		t.Fatal("expected a non-nil control when private egress disabled")
	}
	if err := ctrl("tcp", "127.0.0.1:80", nil); err == nil {
		t.Error("loopback should be blocked")
	}
	if err := ctrl("tcp", "8.8.8.8:80", nil); err != nil {
		t.Errorf("public IP should pass: %v", err)
	}
	// Unparseable address.
	if err := ctrl("tcp", "garbage", nil); err == nil {
		t.Error("unparseable address should be blocked")
	}
	// Non-IP host in address.
	if err := ctrl("tcp", "example.com:80", nil); err == nil {
		t.Error("non-IP host should be blocked by guard")
	}
}

func TestCov_SSRFDialControlNilWhenOptedIn(t *testing.T) {
	SetAllowPrivateEgress(true) // already on via TestMain, assert anyway
	if SSRFDialControl() != nil {
		t.Error("control should be nil when private egress allowed")
	}
}

func TestCov_CheckDialHost(t *testing.T) {
	SetAllowPrivateEgress(false)
	defer SetAllowPrivateEgress(true)

	if err := CheckDialHost("127.0.0.1:5432"); err == nil {
		t.Error("loopback host:port should be blocked")
	}
	if err := CheckDialHost("10.0.0.1"); err == nil {
		t.Error("private bare IP should be blocked")
	}
	if err := CheckDialHost("8.8.8.8:443"); err != nil {
		t.Errorf("public IP should be allowed: %v", err)
	}
	// Hostname that cannot resolve.
	if err := CheckDialHost("nonexistent.invalid.example.:80"); err == nil {
		t.Error("unresolvable host should be blocked")
	}
}

func TestCov_CheckDialHostOptInAllows(t *testing.T) {
	// With opt-in (TestMain default), CheckDialHost is a no-op pass.
	SetAllowPrivateEgress(true)
	if err := CheckDialHost("127.0.0.1:5432"); err != nil {
		t.Errorf("opted-in private egress should allow loopback: %v", err)
	}
}

func TestCov_IsUnsafeIPRanges(t *testing.T) {
	SetAllowPrivateEgress(false)
	defer SetAllowPrivateEgress(true)
	// CGNAT 100.64.0.0/10 is in extraUnsafeCIDRs.
	if err := CheckDialHost("100.64.0.1"); err == nil {
		t.Error("CGNAT address should be blocked")
	}
}

// ===== http_request.go executeHTTPRequest extra paths =================

func TestCov_HTTPNonTextBodyReturnsBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x01, 0x02, 0x03})
	}))
	defer srv.Close()

	res, err := executeHTTPRequest(context.Background(), core.Job{
		Params: map[string]any{"url": srv.URL, "allow_private_networks": true},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if _, ok := res.Output["response_body"].Inline.([]byte); !ok {
		t.Errorf("binary body should stay []byte, got %T", res.Output["response_body"].Inline)
	}
}

func TestCov_HTTPNoContentTypeDefaultsOctet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil // suppress default
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	res, _ := executeHTTPRequest(context.Background(), core.Job{
		Params: map[string]any{"url": srv.URL, "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q", res.Status)
	}
}

func TestCov_HTTPBadHeadersParam(t *testing.T) {
	res, _ := executeHTTPRequest(context.Background(), core.Job{
		Params: map[string]any{"url": "https://x.example", "headers": map[string]any{"A": 1}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

// ===== helpers.go paramHeaders ========================================

func TestCov_ParamHeaders(t *testing.T) {
	// absent.
	h, err := paramHeaders(map[string]any{}, "headers")
	if err != nil || h != nil {
		t.Errorf("absent: h=%v err=%v", h, err)
	}
	// good.
	h2, err := paramHeaders(map[string]any{"headers": map[string]any{"A": "b"}}, "headers")
	if err != nil || h2["A"] != "b" {
		t.Errorf("good: h=%v err=%v", h2, err)
	}
	// wrong outer type.
	if _, err := paramHeaders(map[string]any{"headers": "nope"}, "headers"); err == nil {
		t.Error("wrong outer type should error")
	}
	// non-string value.
	if _, err := paramHeaders(map[string]any{"headers": map[string]any{"A": 1}}, "headers"); err == nil {
		t.Error("non-string value should error")
	}
}

// ===== webhook_send.go encodeWebhookBody / hasHeader ==================

func TestCov_EncodeWebhookBody(t *testing.T) {
	// string with no CT set → uses provided default CT.
	h := map[string]string{}
	b := encodeWebhookBody("text", h, "text/plain")
	if string(b) != "text" || h["Content-Type"] != "text/plain" {
		t.Errorf("string: b=%q h=%v", b, h)
	}
	// string with CT already set → not overwritten.
	h2 := map[string]string{"Content-Type": "text/csv"}
	encodeWebhookBody("x", h2, "text/plain")
	if h2["Content-Type"] != "text/csv" {
		t.Errorf("CT should not be overwritten: %v", h2)
	}
	// []byte.
	h3 := map[string]string{}
	b3 := encodeWebhookBody([]byte("raw"), h3, "application/octet-stream")
	if string(b3) != "raw" || h3["Content-Type"] != "application/octet-stream" {
		t.Errorf("bytes: b=%q h=%v", b3, h3)
	}
	// object → JSON.
	h4 := map[string]string{}
	b4 := encodeWebhookBody(map[string]any{"k": "v"}, h4, "ignored")
	if !strings.Contains(string(b4), `"k":"v"`) || h4["Content-Type"] != "application/json" {
		t.Errorf("object: b=%q h=%v", b4, h4)
	}
}

func TestCov_HasHeader(t *testing.T) {
	h := map[string]string{"Content-Type": "x"}
	if !hasHeader(h, "content-type") {
		t.Error("hasHeader should be case-insensitive")
	}
	if hasHeader(h, "Authorization") {
		t.Error("missing header should be false")
	}
}

func TestCov_WebhookSendHeaderBadParam(t *testing.T) {
	res, _ := executeWebhookSend(context.Background(), core.Job{
		Params: map[string]any{"url": "https://x.example", "headers": map[string]any{"A": 1}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestCov_WebhookSendIdempotencyKeySet(t *testing.T) {
	SetAllowPrivateEgress(true)
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(200)
	}))
	defer srv.Close()
	res, _ := executeWebhookSend(context.Background(), core.Job{
		Params: map[string]any{"url": srv.URL, "body": "x", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q", res.Status)
	}
	if gotKey == "" {
		t.Error("Idempotency-Key should be auto-set")
	}
}

// ===== ratelimit.go SetEgressRateLimit / headerInt / gc / evict ======

func TestCov_SetEgressRateLimitTunes(t *testing.T) {
	// Save & restore the package global so other tests are unaffected.
	saved := egressLimit
	t.Cleanup(func() { egressLimit = saved })

	SetEgressRateLimit(0, 0, 0) // disable; burst/conc fall back to defaults
	if egressLimit.rate > 0 {
		t.Errorf("rate should be <=0 (disabled), got %v", egressLimit.rate)
	}
	if egressLimit.burst != float64(defaultEgressBurst) || egressLimit.conc != defaultEgressConcurrency {
		t.Errorf("non-positive burst/conc should fall back: burst=%v conc=%d", egressLimit.burst, egressLimit.conc)
	}
	// Disabled limiter passes through.
	rel, err := AcquireEgress(context.Background(), "https://api.example.com/x")
	if err != nil {
		t.Fatalf("acquire on disabled: %v", err)
	}
	rel()

	SetEgressRateLimit(600, 10, 4)
	if egressLimit.conc != 4 || egressLimit.burst != 10 {
		t.Errorf("retune failed: burst=%v conc=%d", egressLimit.burst, egressLimit.conc)
	}
}

func TestCov_NewEgressLimiterClamps(t *testing.T) {
	// burst < 1 and conc < 1 are clamped up to 1.
	l := newEgressLimiter(60, 0, 0)
	if l.burst != 1 || l.conc != 1 {
		t.Errorf("burst=%v conc=%d, want both clamped to 1", l.burst, l.conc)
	}
}

func TestCov_HeaderInt(t *testing.T) {
	h := http.Header{}
	h.Set("X-A", "42")
	if v, ok := headerInt(h, "X-Missing", "X-A"); !ok || v != 42 {
		t.Errorf("v=%d ok=%v", v, ok)
	}
	if _, ok := headerInt(h, "X-None"); ok {
		t.Error("missing header should be !ok")
	}
	h.Set("X-Neg", "-1")
	if _, ok := headerInt(h, "X-Neg"); ok {
		t.Error("negative value should be rejected")
	}
	h.Set("X-Bad", "notnum")
	if _, ok := headerInt(h, "X-Bad"); ok {
		t.Error("non-numeric should be rejected")
	}
	if _, ok := headerInt(nil, "X"); ok {
		t.Error("nil header should be !ok")
	}
}

func TestCov_EvictOldestLocked(t *testing.T) {
	l := newEgressLimiter(6000, 100, 10)
	now := time.Now()
	// Two idle buckets; one older, one newer; plus one with an in-flight call.
	l.buckets["old"] = &egressBucket{last: now.Add(-2 * time.Hour)}
	l.buckets["new"] = &egressBucket{last: now}
	l.buckets["busy"] = &egressBucket{last: now.Add(-3 * time.Hour), inflight: 1}
	l.evictOldestLocked()
	if _, ok := l.buckets["old"]; ok {
		t.Error("oldest idle bucket should have been evicted")
	}
	if _, ok := l.buckets["busy"]; !ok {
		t.Error("bucket with in-flight call must not be evicted")
	}
}

func TestCov_GCLockedDropsIdle(t *testing.T) {
	l := newEgressLimiter(6000, 100, 10)
	now := time.Now()
	l.lastGC = now.Add(-2 * time.Minute) // make gc eligible to run
	// Idle, full, no cooldown, untouched > 1 min → eligible for GC.
	l.buckets["idle"] = &egressBucket{tokens: l.burst, last: now.Add(-2 * time.Minute)}
	// Recently used → kept.
	l.buckets["fresh"] = &egressBucket{tokens: l.burst, last: now}
	l.gcLocked(now)
	if _, ok := l.buckets["idle"]; ok {
		t.Error("idle bucket should be GC'd")
	}
	if _, ok := l.buckets["fresh"]; !ok {
		t.Error("fresh bucket should be kept")
	}
}

func TestCov_ResetDelayEpochAndDelta(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("RateLimit-Reset", "30")
	if got := resetDelay(h, now); got != 30*time.Second {
		t.Errorf("delta: got %v", got)
	}
	h2 := http.Header{}
	h2.Set("X-RateLimit-Reset", "0")
	if got := resetDelay(h2, now); got != 0 {
		t.Errorf("zero reset: got %v", got)
	}
	// Epoch in the past → 0.
	h3 := http.Header{}
	h3.Set("RateLimit-Reset", "200000000") // > epochThreshold, but in the past
	if got := resetDelay(h3, now); got != 0 {
		t.Errorf("past epoch: got %v", got)
	}
}

// ===== httpcache.go read/write edge cases =============================

func TestCov_WriteCacheValidatorsNoops(t *testing.T) {
	m, cleanup := memCacheStore()
	defer cleanup()
	// Empty validators → nothing written.
	writeCacheValidators(context.Background(), "t", "name", cacheValidators{})
	if len(m) != 0 {
		t.Errorf("empty validators should not be stored: %v", m)
	}
	// Empty tenant → nothing written.
	writeCacheValidators(context.Background(), "", "name", cacheValidators{ETag: `"x"`})
	if len(m) != 0 {
		t.Errorf("empty tenant should not store: %v", m)
	}
}

func TestCov_ReadCacheValidatorsEdge(t *testing.T) {
	_, cleanup := memCacheStore()
	defer cleanup()
	// Empty tenant → nil.
	if v := readCacheValidators(context.Background(), "", "name"); v != nil {
		t.Errorf("empty tenant should read nil, got %+v", v)
	}
	// Unparseable stored value → nil.
	name := httpCacheName("g", "n", "k")
	cacheMu.RLock()
	w := cacheWriter
	cacheMu.RUnlock()
	_ = w(context.Background(), "t", name, "{not json")
	if v := readCacheValidators(context.Background(), "t", name); v != nil {
		t.Errorf("unparseable value should read nil, got %+v", v)
	}
	// Valid JSON but empty validators → nil.
	_ = w(context.Background(), "t", name, `{}`)
	if v := readCacheValidators(context.Background(), "t", name); v != nil {
		t.Errorf("empty validators should read nil, got %+v", v)
	}
}

func TestCov_HTTPCacheNameFormat(t *testing.T) {
	if got := httpCacheName("g", "n", "k"); got != "httpcache.g.n.k" {
		t.Errorf("got %q", got)
	}
}
