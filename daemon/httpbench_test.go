// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Benchmarks for the request path a browser waits on. They mount the
// routes ONCE, the way ServeListener does, because ServeForTest remounts
// every route per call — benchmarking through it measures route mounting
// rather than the handler.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

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
	h, tok, _ := buildBenchGatewayWith(b, jobstore.NewMemory())
	return h, tok
}

// buildBenchGatewayWith is buildBenchGateway over a caller-supplied job
// store, so a benchmark can put the real Postgres one behind the handler.
// It also returns the Service, for seeding.
func buildBenchGatewayWith(b testing.TB, jobs core.JobStore) (http.Handler, string, *Service) {
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
		Jobs:       jobs,
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        NewMemoryBus(),
		AdminKeys:  ks,
	}
	gw := NewHTTPGateway(svc)
	mux := http.NewServeMux()
	gw.mountRoutes(mux)
	handler := gw.withCORSAndLogging(gw.verifyCookieOrigin(limitRequestBody(gzipResponses(true, jsonErrors(mux)))))
	return handler, token, svc
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

// The run list is the endpoint an open tab polls every two seconds, and the
// only one whose rows used to carry each run's whole stored flow. It needs a
// real Postgres to mean anything: over the in-memory store both paths read
// the same objects, so the projection is free there by construction and the
// cost it removes — transferring and detoasting a JSONB column — does not
// exist. Skips without DAZYFLOW_TEST_DB.
func benchRunListGateway(b *testing.B) (http.Handler, string) {
	b.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		b.Skip("set DAZYFLOW_TEST_DB to run the Postgres request benchmarks")
	}
	ctx := context.Background()
	store, err := jobstore.OpenPostgres(ctx, url)
	if err != nil {
		b.Fatalf("OpenPostgres: %v", err)
	}
	b.Cleanup(store.Close)
	handler, token, _ := buildBenchGatewayWith(b, store)

	// A 40 KB flow is the payload the run pins at submit — a hundred steps
	// with a realistic amount of configuration on each.
	g := core.Graph{ID: "bench-flow", Tenant: "t", Workspace: "ws"}
	for i := range 100 {
		g.Nodes = append(g.Nodes, core.Node{
			ID: fmt.Sprintf("n%d", i), Module: "http_request",
			Params: map[string]any{
				"url":    "https://api.example.com/v1/resource/" + strconv.Itoa(i),
				"method": "POST",
				"body": map[string]any{
					"note": "a realistic amount of configuration on every step, so the stored payload is the size a real flow's is",
					"idx":  i,
				},
			},
		})
	}
	payload, err := json.Marshal(g)
	if err != nil {
		b.Fatalf("marshal graph: %v", err)
	}
	if _, err := store.DeleteByTenant(ctx, "t"); err != nil {
		b.Fatalf("clear tenant: %v", err)
	}
	base := time.Now().Add(-2000 * time.Second)
	for i := range 2000 {
		rec := core.JobRecord{
			ID: fmt.Sprintf("benchrun-%06d", i), Kind: core.JobKindGraph,
			GraphID: "bench-flow", Tenant: "t", Workspace: "ws",
			Status: core.JobStatusSucceeded, GraphPayload: payload,
			EnqueuedAt: base.Add(time.Duration(i) * time.Second),
		}
		if i%7 == 0 {
			rec.Status = core.JobStatusFailed
			rec.Result = &core.Result{Status: core.StatusError,
				Error: &core.JobError{Code: "http_status", Message: "502 from upstream"}}
		}
		if err := store.Enqueue(ctx, rec); err != nil {
			b.Fatalf("seed %d: %v", i, err)
		}
	}
	return handler, token
}

func benchRunListAt(b *testing.B, limit int) {
	handler, token := benchRunListGateway(b)
	path := "/api/v1/me/runs?limit=" + strconv.Itoa(limit)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept-Encoding", "gzip")
		rw := &discardWriter{}
		handler.ServeHTTP(rw, req)
		if rw.status != http.StatusOK {
			b.Fatalf("GET %s = %d", path, rw.status)
		}
	}
}

// BenchmarkRunList20 is the default page the runs view asks for.
func BenchmarkRunList20(b *testing.B) { benchRunListAt(b, 20) }

// BenchmarkRunList200 is the page a tab watching a busy workspace polls:
// RunList re-asks for as many rows as it currently shows.
func BenchmarkRunList200(b *testing.B) { benchRunListAt(b, 200) }

// The run-detail view polls two endpoints every couple of seconds while a
// run is live: the run itself and its node records. Both are scoped by
// loading the run record, which carries the run's whole stored flow.
func benchRunDetailGateway(b *testing.B) (http.Handler, string, string) {
	b.Helper()
	handler, token := benchRunListGateway(b)
	url := os.Getenv("DAZYFLOW_TEST_DB")
	ctx := context.Background()
	store, err := jobstore.OpenPostgres(ctx, url)
	if err != nil {
		b.Fatalf("OpenPostgres: %v", err)
	}
	b.Cleanup(store.Close)
	// Node records for one of the seeded runs, so the detail view has a
	// timeline to render.
	const runID = "benchrun-001000"
	for i := range 100 {
		if err := store.Enqueue(ctx, core.JobRecord{
			ID: fmt.Sprintf("%s-n%d", runID, i), Kind: core.JobKindNode,
			GraphRunID: runID, GraphID: "bench-flow", NodeID: fmt.Sprintf("n%d", i),
			Tenant: "t", Workspace: "ws", Status: core.JobStatusSucceeded,
			Result: &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"body": {Inline: map[string]any{"ok": true, "id": "0123456789abcdef"}},
			}},
		}); err != nil {
			b.Fatalf("seed node %d: %v", i, err)
		}
	}
	return handler, token, runID
}

func benchGET(b *testing.B, handler http.Handler, token, path string) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept-Encoding", "gzip")
		rw := &discardWriter{}
		handler.ServeHTTP(rw, req)
		if rw.status != http.StatusOK {
			b.Fatalf("GET %s = %d", path, rw.status)
		}
	}
}

// BenchmarkGetRun is the run-detail header: seven scalars and an error code.
func BenchmarkGetRun(b *testing.B) {
	handler, token, runID := benchRunDetailGateway(b)
	benchGET(b, handler, token, "/api/v1/me/runs/"+runID)
}

// BenchmarkListRunNodes is the timeline beneath it.
func BenchmarkListRunNodes(b *testing.B) {
	handler, token, runID := benchRunDetailGateway(b)
	benchGET(b, handler, token, "/api/v1/me/runs/"+runID+"/nodes")
}
