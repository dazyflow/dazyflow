// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/engine"
)

// newCollectionShareGateway wires the gateway harness with a sandbox holding
// the seeded `leads` collection and an in-memory link store, so the HTTP
// surface reaches its real path instead of short-circuiting on a nil
// dependency.
func newCollectionShareGateway(t *testing.T) (*gatewayHarness, *memCollectionShareStore) {
	t.Helper()
	h := newGatewayHarness(t)
	sb, err := NewFSSandbox(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSSandbox: %v", err)
	}
	h.svc.Engine = &engine.Engine{Sandbox: sb, Resolver: &engine.NodeResolver{Native: engine.Default}}
	seedBoardStore(t, sb, "t", "ws")
	shares := newMemCollectionShareStore()
	h.svc.CollectionShares = shares
	return h, shares
}

// anonGet hits a path with no Authorization header — the public surface.
func anonGet(t *testing.T, h *gatewayHarness, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestHTTPCollectionShare_MintCopyRevoke(t *testing.T) {
	t.Parallel()
	h, _ := newCollectionShareGateway(t)

	// Nothing published yet.
	if rw := h.do(t, http.MethodGet, "/api/v1/me/collection-shares/leads", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("GET before minting: status %d, body %s", rw.Code, rw.Body.String())
	}

	rw := h.do(t, http.MethodPost, "/api/v1/me/collection-shares/leads", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("POST: status %d, body %s", rw.Code, rw.Body.String())
	}
	var link struct {
		Collection string `json:"collection"`
		Token      string `json:"token"`
		URL        string `json:"url"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &link); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if link.Token == "" || link.Collection != "leads" {
		t.Fatalf("link = %+v", link)
	}
	// The URL must be the page a person can open, not the API path.
	if !strings.HasSuffix(link.URL, "/board/"+link.Token) {
		t.Errorf("url = %q, want a /board/<token> page", link.URL)
	}

	// It now shows up in the listing the Collections page reads.
	rw = h.do(t, http.MethodGet, "/api/v1/me/collection-shares", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list: status %d", rw.Code)
	}
	var list struct {
		Shares []struct{ Collection string } `json:"shares"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Shares) != 1 || list.Shares[0].Collection != "leads" {
		t.Fatalf("list = %+v", list.Shares)
	}

	// And revoking takes the link down.
	if rw := h.do(t, http.MethodDelete, "/api/v1/me/collection-shares/leads", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d, body %s", rw.Code, rw.Body.String())
	}
	if rw := anonGet(t, h, "/api/v1/public/collection/"+link.Token); rw.Code != http.StatusNotFound {
		t.Errorf("revoked token still served: status %d", rw.Code)
	}
}

// The public page takes no credential but the token, and its body is the
// collection's own rows.
func TestHTTPCollectionShare_PublicReadIsUnauthenticated(t *testing.T) {
	t.Parallel()
	h, shares := newCollectionShareGateway(t)
	sh, err := shares.Upsert(t.Context(), "t", "ws", "leads", "tok-abc", "alice")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rw := anonGet(t, h, "/api/v1/public/collection/"+sh.Token)
	if rw.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rw.Code, rw.Body.String())
	}
	var got PublicCollectionData
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Collection != "leads" || got.Total != 2 || len(got.Rows) != 2 {
		t.Fatalf("data = %+v", got)
	}

	// This body is real data behind a secret URL, so it must not be cacheable
	// by anything in the path — unlike the sanitized TV snapshot next door,
	// which is deliberately `public, max-age=2`.
	if cc := rw.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if strings.Contains(rw.Header().Get("Cache-Control"), "public") {
		t.Errorf("Cache-Control = %q, must not allow shared caches",
			rw.Header().Get("Cache-Control"))
	}
	// The row-delete handle stays inside the app.
	if strings.Contains(rw.Body.String(), boardRowIDKey) {
		t.Errorf("public body leaked %s: %s", boardRowIDKey, rw.Body.String())
	}
}

// A wrong or rotated token is a flat 404 that says nothing about whether the
// workspace or the collection exists.
func TestHTTPCollectionShare_UnknownTokenIs404(t *testing.T) {
	t.Parallel()
	h, _ := newCollectionShareGateway(t)
	for _, tok := range []string{"nope", "0123456789abcdef"} {
		rw := anonGet(t, h, "/api/v1/public/collection/"+tok)
		if rw.Code != http.StatusNotFound {
			t.Errorf("token %q: status %d, want 404", tok, rw.Code)
		}
		if !strings.Contains(rw.Body.String(), "share_not_found") {
			t.Errorf("token %q: body %s, want share_not_found", tok, rw.Body.String())
		}
	}
}

func TestHTTPCollectionShare_UnknownCollectionIs404(t *testing.T) {
	t.Parallel()
	h, _ := newCollectionShareGateway(t)
	rw := h.do(t, http.MethodPost, "/api/v1/me/collection-shares/leeds", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("status %d, body %s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "board_not_found") {
		t.Errorf("body %s, want board_not_found", rw.Body.String())
	}
}

// A deployment without the link store reports not-configured on the
// authenticated surface, and an unknown link on the public one — never a 500.
func TestHTTPCollectionShare_NoStore(t *testing.T) {
	t.Parallel()
	h, _ := newCollectionShareGateway(t)
	h.svc.CollectionShares = nil

	if rw := h.do(t, http.MethodPost, "/api/v1/me/collection-shares/leads", nil); rw.Code != http.StatusNotImplemented {
		t.Errorf("POST: status %d, want 501", rw.Code)
	}
	if rw := anonGet(t, h, "/api/v1/public/collection/anything"); rw.Code != http.StatusNotFound {
		t.Errorf("public GET: status %d, want 404", rw.Code)
	}
	// A listing still answers — "no links exist" is true and renderable.
	if rw := h.do(t, http.MethodGet, "/api/v1/me/collection-shares", nil); rw.Code != http.StatusOK {
		t.Errorf("list: status %d, want 200", rw.Code)
	}
}
