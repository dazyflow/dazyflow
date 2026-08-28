// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// newRawReq builds a Bearer-authenticated request with an arbitrary (possibly
// malformed) raw string body — used to exercise decode-error branches that the
// JSON-marshalling h.do helper can't reach.
func newRawReq(t *testing.T, h *gatewayHarness, method, path, rawBody string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(rawBody))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func serveRaw(h *gatewayHarness, req *http.Request) *httptest.ResponseRecorder {
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

// runOnlyHarness wraps a gateway harness whose token carries only graph:run —
// it can run a flow but not edit/admin it, so admin-gated handlers must 403.
type runOnlyHarness struct {
	*gatewayHarness
	runToken string
}

func newRunOnlyHarness(t *testing.T) *runOnlyHarness {
	t.Helper()
	h := newGatewayHarness(t)
	role := core.Role{Name: "runner", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-run", "t", "ws", "runner", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue run key: %v", err)
	}
	return &runOnlyHarness{gatewayHarness: h, runToken: tok}
}

func runOnlyDo(t *testing.T, h *runOnlyHarness, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	// Temporarily swap the token so h.do uses the run-only credential.
	saved := h.gatewayHarness.token
	h.gatewayHarness.token = h.runToken
	defer func() { h.gatewayHarness.token = saved }()
	return h.gatewayHarness.do(t, method, path, body)
}
