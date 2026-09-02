// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"testing"
)

// HTTP-handler error branches of the run-lifecycle endpoints
// (cancel/resume/retry). The service-level happy paths live in
// cancel_test.go / resume_test.go / retry_test.go.

func TestCancelRunMe_NotFound(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/runs/ghostrun/cancel", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown run = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}

func TestCancelRunMe_MalformedBody(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	req := newRawReq(t, h, "POST", "/api/v1/me/runs/ghostrun/cancel", "{not json")
	req.ContentLength = int64(len("{not json"))
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("cancel malformed = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestResumeRunMe_NotFound(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/runs/ghostrun/resume", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("resume unknown run = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}

func TestResumeRunMe_MalformedBody(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	req := newRawReq(t, h, "POST", "/api/v1/me/runs/ghostrun/resume", "{not json")
	req.ContentLength = int64(len("{not json"))
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("resume malformed = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestRetryRunMe_NotFound(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/runs/ghostrun/retry", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("retry unknown run = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}
