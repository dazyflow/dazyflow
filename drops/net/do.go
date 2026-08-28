// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultMaxResponseBytes is the fallback response cap when a caller passes
// maxBytes <= 0. Connectors normally pass their own per-API cap.
const defaultMaxResponseBytes = 16 << 20 // 16 MiB

// Do runs one guarded outbound HTTP call — the shared epilogue every connector
// needs, factored out of the dozen near-identical `<vendor>Do` helpers. It:
//   - bounds the request with a timeout (timeoutMS; <=0 falls back to 30s),
//   - runs the SSRF dial guard + egress allowlist (EgressAllowedFor): URLs can
//     be tenant-supplied via base_url params, so a bearer token / API key must
//     not be exfiltrable to a loopback/private/link-local or off-allowlist host,
//   - caps the response body at maxBytes so a hostile or buggy upstream can't
//     OOM the daemon (rejected with an error whose message contains "exceeds").
//
// It returns the status, the capped body, and the response header, and
// deliberately does NOT interpret the result: vendor error shapes and
// pagination headers differ, so the caller classifies. Callers supply their own
// auth/content-type/idempotency via headers and their per-API maxBytes. This is
// the connector sibling of llmtask.PostJSON.
func Do(ctx context.Context, method, url string, headers map[string]string, body []byte, timeoutMS, maxBytes int) (int, []byte, http.Header, error) {
	if timeoutMS <= 0 {
		timeoutMS = 30000
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Guard the dial against the ORIGINAL ctx (not reqCtx): base_url can arrive
	// via API/test params, so the SSRF client blocks loopback/private/link-local
	// targets and the egress allowlist (when set) bounds which public hosts the
	// credentials may reach.
	if err := EgressAllowedFor(ctx, url); err != nil {
		return 0, nil, nil, err
	}
	// Pace + bound the call per (tenant, host): a shared egress fleet must
	// not let one tenant's burst exhaust a third-party API's budget or get
	// the platform's egress IP throttled for everyone. Blocks (honoring ctx)
	// until a token + concurrency slot is free and any prior 429 cooldown for
	// this host has elapsed.
	release, lerr := AcquireEgress(ctx, url)
	if lerr != nil {
		return 0, nil, nil, lerr
	}
	defer release()
	resp, err := SafeHTTPClient(timeout, PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	// Record rate-limit signals (429/Retry-After/RateLimit-*) so the next
	// call to this host self-paces and the worker's retry honors Retry-After.
	ObserveEgressResponse(ctx, url, resp.StatusCode, resp.Header)

	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	limit := int64(maxBytes)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, nil, resp.Header, err
	}
	if int64(len(raw)) > limit {
		return resp.StatusCode, nil, resp.Header, fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	return resp.StatusCode, raw, resp.Header, nil
}
