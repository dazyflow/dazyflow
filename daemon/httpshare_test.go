// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// inlineShareStore is a tiny in-memory ShareStore for the gateway HTTP tests.
type inlineShareStore struct {
	mu sync.Mutex
	m  map[string]Share
}

func newInlineShareStore() *inlineShareStore { return &inlineShareStore{m: map[string]Share{}} }
func (s *inlineShareStore) k(t, w string) string {
	return t + "/" + w
}
func (s *inlineShareStore) Get(_ context.Context, t, w string) (Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.m[s.k(t, w)]; ok {
		return sh, nil
	}
	return Share{}, core.ErrNotFound
}
func (s *inlineShareStore) Upsert(_ context.Context, t, w, token, by string) (Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := Share{Tenant: t, Workspace: w, Token: token, CreatedAt: time.Now(), CreatedBy: by}
	s.m[s.k(t, w)] = sh
	return sh, nil
}
func (s *inlineShareStore) Delete(_ context.Context, t, w string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, s.k(t, w))
	return nil
}
func (s *inlineShareStore) Lookup(_ context.Context, token string) (Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sh := range s.m {
		if sh.Token == token {
			return sh, nil
		}
	}
	return Share{}, core.ErrNotFound
}
func (s *inlineShareStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	return 0, nil
}

// anon issues an unauthenticated request — used to prove the public overview
// endpoint needs no credential.
func (h *gatewayHarness) anon(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

// TestHTTPGateway_ShareLifecycle drives the full wire surface: create the
// link (authed), read it back, hit the snapshot anonymously, then revoke it
// and confirm the public link goes dead.
func TestHTTPGateway_ShareLifecycle(t *testing.T) {
	h := newGatewayHarness(t)
	h.svc.Shares = newInlineShareStore()

	// A visible flow so the public board has a tile.
	if rw := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Fgreet", core.Graph{
		ID: "greet", Tenant: "t", Workspace: "ws", Name: "Greeter",
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}); rw.Code != http.StatusOK {
		t.Fatalf("create flow: code=%d body=%s", rw.Code, rw.Body.String())
	}
	// Only published flows surface on the board now, so publish it.
	if rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fgreet/publish", nil); rw.Code != http.StatusOK {
		t.Fatalf("publish flow: code=%d body=%s", rw.Code, rw.Body.String())
	}

	// Mint the share link.
	rw := h.do(t, "POST", "/api/v1/me/share?tenant=t&workspace=ws", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("create share: code=%d body=%s", rw.Code, rw.Body.String())
	}
	var created shareResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	if created.Token == "" || created.URL == "" {
		t.Fatalf("expected token + url, got %+v", created)
	}

	// GET returns the same token.
	rw = h.do(t, "GET", "/api/v1/me/share?tenant=t&workspace=ws", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get share: code=%d body=%s", rw.Code, rw.Body.String())
	}
	var got shareResponse
	_ = json.Unmarshal(rw.Body.Bytes(), &got)
	if got.Token != created.Token {
		t.Fatalf("get token %q != created %q", got.Token, created.Token)
	}

	// The public snapshot resolves WITHOUT any Authorization header.
	rw = h.anon(t, "GET", "/api/v1/public/overview/"+created.Token)
	if rw.Code != http.StatusOK {
		t.Fatalf("public overview (anon): code=%d body=%s", rw.Code, rw.Body.String())
	}
	var data PublicOverviewData
	if err := json.Unmarshal(rw.Body.Bytes(), &data); err != nil {
		t.Fatalf("unmarshal overview: %v", err)
	}
	if len(data.Flows) != 1 || data.Flows[0].Name != "Greeter" {
		t.Fatalf("expected one Greeter tile, got %+v", data.Flows)
	}
	// Sanitization: the wire bytes must not carry tenant/workspace identifiers.
	if body := rw.Body.String(); strings.Contains(body, `"tenant"`) || strings.Contains(body, `"workspace"`) {
		t.Fatalf("public payload leaked tenant/workspace: %s", body)
	}

	// An unknown token is a flat 404.
	if rw := h.anon(t, "GET", "/api/v1/public/overview/bogus"); rw.Code != http.StatusNotFound {
		t.Fatalf("bogus token: code=%d, want 404", rw.Code)
	}

	// Revoke, then the public link is dead.
	if rw := h.do(t, "DELETE", "/api/v1/me/share?tenant=t&workspace=ws", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("delete share: code=%d body=%s", rw.Code, rw.Body.String())
	}
	if rw := h.anon(t, "GET", "/api/v1/public/overview/"+created.Token); rw.Code != http.StatusNotFound {
		t.Fatalf("after revoke: code=%d, want 404", rw.Code)
	}
}
