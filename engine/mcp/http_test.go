// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

// fakeHTTPServer is an MCP endpoint over streamable HTTP. It answers
// initialize/tools/list/tools/call and can be told to reply as SSE instead of
// JSON, which is the shape a real server picks for a long-running call.
type fakeHTTPServer struct {
	tools []mcp.Tool
	// sse makes every response an event stream.
	sse bool
	// authSeen records the Authorization (or custom) header of the last
	// request, so a test can assert the credential actually left the process.
	authSeen atomic.Value
	calls    atomic.Int64
	// toolErr makes tools/call answer with isError.
	toolErr bool
	// recordArgs stores the arguments of the last tools/call, so a test can
	// assert what actually reached the tool rather than only that it was
	// called.
	recordArgs bool
	lastArgs   atomic.Value
	// status, when non-zero, short-circuits every request with that code.
	status int
}

func (f *fakeHTTPServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if f.status != 0 {
			http.Error(w, "nope", f.status)
			return
		}
		f.authSeen.Store(r.Header.Get("Authorization") + "|" + r.Header.Get("X-Api-Key"))

		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		// A notification has no id and expects no response.
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake", "version": "9"},
			}
		case "tools/list":
			result = map[string]any{"tools": f.tools}
		case "tools/call":
			f.calls.Add(1)
			if f.recordArgs {
				var p struct {
					Arguments json.RawMessage `json:"arguments"`
				}
				_ = json.Unmarshal(req.Params, &p)
				f.lastArgs.Store(string(p.Arguments))
			}
			result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": "pong"}},
				"isError": f.toolErr,
			}
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
			return
		}

		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if !f.sse {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		// SSE: a keepalive comment and an unrelated notification first, so the
		// reader has to skip both to find the response.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
	}
}

func newFakeHTTP(t *testing.T, f *fakeHTTPServer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return srv
}

func echoTool() mcp.Tool {
	return mcp.Tool{Name: "echo", Description: "echo it back", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

// TestRegisterHTTP_JSON is the ordinary path: handshake, tool list, and a call
// that comes back as a single JSON object.
func TestRegisterHTTP_JSON(t *testing.T) {
	fake := &fakeHTTPServer{tools: []mcp.Tool{echoTool()}}
	srv := newFakeHTTP(t, fake)

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "demo", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}

	tr, ok := cat.Get("acme", "mcp:demo:echo")
	if !ok {
		t.Fatal("tool not registered for its own tenant")
	}
	if got := tr.Manifest().ID; got != "mcp:demo:echo" {
		t.Fatalf("manifest id = %q", got)
	}
	res, err := tr.Execute(context.Background(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q (%+v)", res.Status, res.Error)
	}
	if fake.calls.Load() != 1 {
		t.Fatalf("tools/call count = %d", fake.calls.Load())
	}
}

// TestRegisterHTTP_SSE covers the other body shape. The response has to be
// found among a keepalive and an unrelated notification.
func TestRegisterHTTP_SSE(t *testing.T) {
	fake := &fakeHTTPServer{tools: []mcp.Tool{echoTool()}, sse: true}
	srv := newFakeHTTP(t, fake)

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "demo", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP over SSE: %v", err)
	}
	tr, ok := cat.Get("acme", "mcp:demo:echo")
	if !ok {
		t.Fatal("tool not registered")
	}
	res, err := tr.Execute(context.Background(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q", res.Status)
	}
}

// TestRegisterHTTP_TenantIsolation is the security property: one org's server
// is not reachable from another, and not reachable with no tenant at all.
func TestRegisterHTTP_TenantIsolation(t *testing.T) {
	srv := newFakeHTTP(t, &fakeHTTPServer{tools: []mcp.Tool{echoTool()}})

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "demo", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}

	if _, ok := cat.Get("other", "mcp:demo:echo"); ok {
		t.Fatal("another tenant resolved acme's MCP tool")
	}
	if _, ok := cat.Get("", "mcp:demo:echo"); ok {
		t.Fatal("a tenant-less lookup resolved a tenant's MCP tool")
	}
	if m := cat.ManifestsFor("other"); len(m) != 0 {
		t.Fatalf("another tenant sees %d manifest(s): %v", len(m), m)
	}
	if m := cat.ManifestsFor("acme"); len(m) != 1 {
		t.Fatalf("owning tenant sees %d manifest(s), want 1", len(m))
	}
}

// TestRegisterHTTP_InstanceWideVisibleToAll covers the other half: an
// operator's server (tenant "") is every org's to use.
func TestRegisterHTTP_InstanceWideVisibleToAll(t *testing.T) {
	srv := newFakeHTTP(t, &fakeHTTPServer{tools: []mcp.Tool{echoTool()}})

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "shared", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	for _, tenant := range []string{"acme", "globex", ""} {
		if _, ok := cat.Get(tenant, "mcp:shared:echo"); !ok {
			t.Fatalf("tenant %q cannot see the instance-wide server", tenant)
		}
	}
}

// TestRegisterHTTP_RefusesShadowingInstanceWide: an org may not take a name
// the operator already uses, because the palette and the executor would then
// disagree about which server a step id means.
func TestRegisterHTTP_RefusesShadowingInstanceWide(t *testing.T) {
	srv := newFakeHTTP(t, &fakeHTTPServer{tools: []mcp.Tool{echoTool()}})

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "github", URL: srv.URL}); err != nil {
		t.Fatalf("instance-wide RegisterHTTP: %v", err)
	}
	err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "github", Tenant: "acme", URL: srv.URL})
	if err == nil {
		t.Fatal("a tenant was allowed to shadow an instance-wide server name")
	}
	if !strings.Contains(err.Error(), "every org") {
		t.Fatalf("error does not explain the clash: %v", err)
	}
}

// TestRegisterHTTP_ReplacesOnReRegister is what saving an edit does: the same
// (tenant, name) reconnects with the new configuration instead of erroring.
func TestRegisterHTTP_ReplacesOnReRegister(t *testing.T) {
	first := newFakeHTTP(t, &fakeHTTPServer{tools: []mcp.Tool{echoTool()}})
	second := newFakeHTTP(t, &fakeHTTPServer{tools: []mcp.Tool{{Name: "other"}}})

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	desc := mcp.HTTPDescriptor{Name: "demo", Tenant: "acme", URL: first.URL}
	if err := cat.RegisterHTTP(desc); err != nil {
		t.Fatalf("first register: %v", err)
	}
	desc.URL = second.URL
	if err := cat.RegisterHTTP(desc); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if _, ok := cat.Get("acme", "mcp:demo:echo"); ok {
		t.Fatal("the replaced server's tool is still registered")
	}
	if _, ok := cat.Get("acme", "mcp:demo:other"); !ok {
		t.Fatal("the new server's tool is missing")
	}
}

// TestRegisterHTTP_SendsCredential proves the configured auth header reaches
// the server — the whole point of storing one.
func TestRegisterHTTP_SendsCredential(t *testing.T) {
	fake := &fakeHTTPServer{tools: []mcp.Tool{echoTool()}}
	srv := newFakeHTTP(t, fake)

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer sekrit")
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "demo", Tenant: "acme", URL: srv.URL, Header: hdr}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	seen, _ := fake.authSeen.Load().(string)
	if !strings.HasPrefix(seen, "Bearer sekrit|") {
		t.Fatalf("server saw %q, want the bearer token", seen)
	}
}

// TestRegisterHTTP_UnauthorizedIsExplained: a 401 is the most likely failure
// an admin will hit, so it must not surface as a bare status code.
func TestRegisterHTTP_UnauthorizedIsExplained(t *testing.T) {
	srv := newFakeHTTP(t, &fakeHTTPServer{status: http.StatusUnauthorized})

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "demo", Tenant: "acme", URL: srv.URL})
	if err == nil {
		t.Fatal("registering against a 401 endpoint succeeded")
	}
	if !strings.Contains(err.Error(), "refused the credential") {
		t.Fatalf("unhelpful error: %v", err)
	}
	if _, ok := cat.Get("acme", "mcp:demo:echo"); ok {
		t.Fatal("a failed registration left tools behind")
	}
}

// TestUnregister removes the server and its tools.
func TestUnregister(t *testing.T) {
	srv := newFakeHTTP(t, &fakeHTTPServer{tools: []mcp.Tool{echoTool()}})

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "demo", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	cat.Unregister("acme", "demo")
	if _, ok := cat.Get("acme", "mcp:demo:echo"); ok {
		t.Fatal("tool still resolves after Unregister")
	}
	if got := cat.ServersFor("acme"); len(got) != 0 {
		t.Fatalf("ServersFor still lists %d server(s)", len(got))
	}
	// Unregistering something absent is not an error — see Unregister.
	cat.Unregister("acme", "never-existed")
}

// TestAllManifests_IncludesTenantServers is what the platform killswitch page
// reads; a tenant server missing from it cannot be switched off.
func TestAllManifests_IncludesTenantServers(t *testing.T) {
	srv := newFakeHTTP(t, &fakeHTTPServer{tools: []mcp.Tool{echoTool()}})

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "demo", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	manifests, tenants := cat.AllManifests()
	if _, ok := manifests["mcp:demo:echo"]; !ok {
		t.Fatal("tenant tool missing from AllManifests")
	}
	if got := tenants["mcp:demo:echo"]; len(got) != 1 || got[0] != "acme" {
		t.Fatalf("owning tenants = %v, want [acme]", got)
	}
}
