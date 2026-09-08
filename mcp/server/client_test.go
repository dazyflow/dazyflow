// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/mcp/server"
)

func TestClient_Whoami(t *testing.T) {
	fake, srv := newFakeHzd()
	t.Cleanup(srv.Close)
	fake.on("GET", "/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"subject":"u1","tenant":"t","workspace":"ws","permissions":["read"]}`)
	})
	c := server.NewDazydClient(srv.URL, "tok")
	w, err := c.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if w.Tenant != "t" || w.Workspace != "ws" || w.Subject != "u1" {
		t.Errorf("whoami = %+v", w)
	}
	if len(fake.requests) != 1 || fake.requests[0].auth != "Bearer tok" {
		t.Errorf("requests = %+v", fake.requests)
	}
}

func TestClient_Whoami_Error(t *testing.T) {
	fake, srv := newFakeHzd()
	t.Cleanup(srv.Close)
	fake.on("GET", "/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	c := server.NewDazydClient(srv.URL, "tok")
	if _, err := c.Whoami(context.Background()); err == nil {
		t.Fatal("expected error from 401")
	}
}

func TestClient_Verbs(t *testing.T) {
	fake, srv := newFakeHzd()
	t.Cleanup(srv.Close)
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		fake.on(m, "/api/v1/thing", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"ok":true}`)
		})
	}
	c := server.NewDazydClient(srv.URL, "")
	ctx := context.Background()

	var out map[string]any
	if err := c.Get(ctx, "/thing", &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := c.Post(ctx, "/thing", map[string]any{"a": 1}, &out); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if err := c.Put(ctx, "/thing", map[string]any{"b": 2}, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Patch(ctx, "/thing", map[string]any{"c": 3}, &out); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if err := c.Delete(ctx, "/thing"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, r := range fake.requests {
		if r.auth != "" {
			t.Errorf("expected no auth header for empty token, got %q", r.auth)
		}
	}
}

func TestClient_Delete_Error(t *testing.T) {
	fake, srv := newFakeHzd()
	t.Cleanup(srv.Close)
	fake.on("DELETE", "/api/v1/thing", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	})
	c := server.NewDazydClient(srv.URL, "tok")
	if err := c.Delete(context.Background(), "/thing"); err == nil {
		t.Fatal("expected error from 409")
	}
}

func TestClient_DecodeError(t *testing.T) {
	fake, srv := newFakeHzd()
	t.Cleanup(srv.Close)
	fake.on("GET", "/api/v1/thing", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `not json`)
	})
	c := server.NewDazydClient(srv.URL, "tok")
	var out map[string]any
	err := c.Get(context.Background(), "/thing", &out)
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

// Covers the do() branch where the HTTP round trip itself fails (connection
// refused), which becomes a non-HTTPError and must surface as an RPC error
// through the tools.
func TestClient_TransportError(t *testing.T) {
	_, srv := newFakeHzd()
	url := srv.URL
	srv.Close()
	c := server.NewDazydClient(url, "tok")
	var out map[string]any
	if err := c.Get(context.Background(), "/thing", &out); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestClient_HTTPError_ErrorString(t *testing.T) {
	fake, srv := newFakeHzd()
	t.Cleanup(srv.Close)

	fake.on("GET", "/api/v1/coded", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"flow_locked","message":"locked"}}`)
	})
	fake.on("GET", "/api/v1/plain", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	c := server.NewDazydClient(srv.URL, "tok")
	var out map[string]any

	err := c.Get(context.Background(), "/coded", &out)
	var he *server.HTTPError
	if !asHTTPError(err, &he) {
		t.Fatalf("want *HTTPError, got %T", err)
	}
	if !strings.Contains(he.Error(), "code: flow_locked") {
		t.Errorf("coded Error() = %q", he.Error())
	}
	p := he.ToToolPayload()
	if p["code"] != "flow_locked" || p["status"].(int) != 409 {
		t.Errorf("payload = %+v", p)
	}

	err = c.Get(context.Background(), "/plain", &out)
	if !asHTTPError(err, &he) {
		t.Fatalf("want *HTTPError, got %T", err)
	}
	if !strings.Contains(he.Error(), "boom") {
		t.Errorf("plain Error() = %q", he.Error())
	}
}

func asHTTPError(err error, target **server.HTTPError) bool {
	for err != nil {
		if he, ok := err.(*server.HTTPError); ok {
			*target = he
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestClient_HTTPError_DetailsInPayload(t *testing.T) {
	s, fake, _ := fullStack(t)
	fake.on("POST", "/api/v1/validate/graph", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_graph","message":"bad","details":[{"field":"nodes","issue":"empty"}],"doc":"/d"}}`)
	})
	res := runToolCall(t, s, "validate_graph", map[string]any{"graph": map[string]any{}})
	if !res.IsError {
		t.Fatalf("expected tool-error, got success: %s", res.Content[0].Text)
	}
	for _, want := range []string{"invalid_graph", "nodes", "empty", "/d"} {
		if !strings.Contains(res.Content[0].Text, want) {
			t.Errorf("payload missing %q: %s", want, res.Content[0].Text)
		}
	}
}
