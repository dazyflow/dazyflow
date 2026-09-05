// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Benchmarks for the request path a browser waits on. They mount the
// routes ONCE, the way ServeListener does, because ServeForTest remounts
// every route per call — benchmarking through it measures route mounting
// rather than the handler.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// benchGateway builds the same gateway newGatewayHarness does and returns
// the production handler stack with routes mounted once, plus a token.
func benchGateway(b *testing.B) (http.Handler, string) {
	b.Helper()
	return buildBenchGateway(b)
}

func benchGatewayT(t *testing.T) (http.Handler, string) { return buildBenchGateway(t) }

func buildBenchGateway(b testing.TB) (http.Handler, string) {
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, token, err := auth.IssueAPIKey(ks, b.Context(), "k1", "t", "ws", "alice", []core.Role{role}, nil)
	if err != nil {
		b.Fatalf("issue key: %v", err)
	}
	wsStore, _ := workspace.OpenFS("")
	svc := &Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: MapWorkspaces{"t/ws": wsStore},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        NewMemoryBus(),
		AdminKeys:  ks,
	}
	gw := NewHTTPGateway(svc)
	mux := http.NewServeMux()
	gw.mountRoutes(mux)
	handler := gw.withCORSAndLogging(gw.verifyCookieOrigin(limitRequestBody(gzipResponses(true, jsonErrors(mux)))))
	return handler, token
}

func benchRequest(b *testing.B, method, path string) {
	handler, token := benchGateway(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)
		if rw.Code != http.StatusOK {
			b.Fatalf("%s %s = %d: %s", method, path, rw.Code, rw.Body.String())
		}
	}
}

// BenchmarkGetMe is the smallest authenticated request there is: it
// stands in for the fixed cost every API call pays before its own work.
func BenchmarkGetMe(b *testing.B) { benchRequest(b, "GET", "/api/v1/me") }

// BenchmarkListDrops is the catalog request the flow editor's palette
// makes, over the real built-in catalog.
func BenchmarkListDrops(b *testing.B) { benchRequest(b, "GET", "/api/v1/drops") }

// BenchmarkMountRoutes measures building the router. Production does this
// once per process, but every ServeForTest call in the test suite repeats
// it, so it also prices the suite's per-request overhead.
func BenchmarkMountRoutes(b *testing.B) {
	handlerGW, _ := benchGateway(b)
	_ = handlerGW
	ks := auth.NewMemKeyStore()
	svc := &Service{
		Auth:      auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Jobs:      jobstore.NewMemory(),
		Engine:    &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:       NewMemoryBus(),
		AdminKeys: ks,
	}
	gw := NewHTTPGateway(svc)
	b.ReportAllocs()
	for b.Loop() {
		gw.mountRoutes(http.NewServeMux())
	}
}

// discardWriter is a ResponseWriter that throws the body away, so a
// benchmark measures producing the response rather than the test
// recorder's own 1 MB of buffer growth. It is the closer analogue of a
// socket.
type discardWriter struct {
	h      http.Header
	status int
}

func (d *discardWriter) Header() http.Header {
	if d.h == nil {
		d.h = make(http.Header)
	}
	return d.h
}
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(status int)      { d.status = status }

func benchRequestDiscard(b *testing.B, path, acceptEncoding string) {
	handler, token := benchGateway(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}
		rw := &discardWriter{}
		handler.ServeHTTP(rw, req)
		if rw.status != http.StatusOK {
			b.Fatalf("GET %s = %d", path, rw.status)
		}
	}
}

// BenchmarkListDropsDiscard is the palette as the server actually pays
// for it: body produced, nothing accumulated.
func BenchmarkListDropsDiscard(b *testing.B) { benchRequestDiscard(b, "/api/v1/drops", "") }

// BenchmarkListDropsGzip adds what a real browser sends, so the cost of
// compressing the catalog is visible next to the bytes it saves.
func BenchmarkListDropsGzip(b *testing.B) { benchRequestDiscard(b, "/api/v1/drops", "gzip") }

// BenchmarkListDropsRevalidate is the palette request a browser that
// already has the catalog makes: conditional, and answered with a 304.
func BenchmarkListDropsRevalidate(b *testing.B) {
	handler, token := benchGateway(b)
	// One unconditional request to learn the current tag.
	warm := httptest.NewRequest("GET", "/api/v1/drops", nil)
	warm.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, warm)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		b.Fatal("no ETag on the catalog response")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest("GET", "/api/v1/drops", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("If-None-Match", etag)
		rw := &discardWriter{}
		handler.ServeHTTP(rw, req)
		if rw.status != http.StatusNotModified {
			b.Fatalf("GET /api/v1/drops conditional = %d, want 304", rw.status)
		}
	}
}
