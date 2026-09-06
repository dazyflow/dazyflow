// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Benchmarks for the request path a browser waits on. They mount the
// routes ONCE, the way ServeListener does, because ServeForTest remounts
// every route per call — benchmarking through it measures route mounting
// rather than the handler.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
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
	benchLastGateway = gw
	mux := http.NewServeMux()
	gw.mountRoutes(mux)
	handler := gw.withCORSAndLogging(gw.verifyCookieOrigin(limitRequestBody(gzipResponses(true, jsonErrors(mux)))))
	return handler, token, svc
}

// benchLastGateway is the gateway the most recent builder produced, so a
// benchmark can reach past the handler to the gateway's own knobs. Used by the
// webhook benchmark to lift the per-IP throttle: every request there comes
// from the same synthetic address, so the limiter — which exists to stop a
// flood from one caller — would otherwise be the only thing measured.
var benchLastGateway *HTTPGateway

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

// The approvals badge in the sidebar polls every 30 seconds on every page,
// for every signed-in browser, and re-fires on each navigation — so it is
// the most repeated authenticated request the product makes. It renders one
// integer. These benchmarks measure what producing that integer costs.
//
// Postgres-gated for the same reason the run-list ones are: over the memory
// store both paths read the same objects, so the cost this is about —
// transferring and decoding each parked step's stashed context — does not
// exist there.
func benchApprovalsGateway(b *testing.B, pending int) (http.Handler, string) {
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
	if _, err := store.DeleteByTenant(ctx, "t"); err != nil {
		b.Fatalf("clear tenant: %v", err)
	}
	// A parked approval stashes whatever the flow wired into its Value port,
	// which is what the inbox renders — an order, a refund, a draft reply.
	// Sized just under the 4 KB preview cap, because that is the case the
	// list path pays for in full.
	approvalCtx := map[string]any{
		"order_id": "ord_01HQ8ZK3M9",
		"customer": map[string]any{"name": "Ada Lovelace", "email": "ada@example.com"},
		"total":    "1249.00",
		"reason":   strings.Repeat("customer requested a refund; agent notes follow. ", 60),
	}
	for i := range pending {
		if err := store.Enqueue(ctx, core.JobRecord{
			ID: fmt.Sprintf("appr-%06d", i), Kind: core.JobKindNode,
			GraphRunID: fmt.Sprintf("apprun-%06d", i), GraphID: "bench-flow",
			NodeID: "approve", Tenant: "t", Workspace: "ws",
			Status: core.JobStatusAwaiting,
			Result: &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"pending_url": {Inline: "https://dazyflow.example/approve/apprun-" + strconv.Itoa(i)},
				"prompt":      {Inline: "Approve this refund?"},
				"context":     {Inline: approvalCtx},
			}},
		}); err != nil {
			b.Fatalf("seed approval %d: %v", i, err)
		}
	}
	return handler, token
}

func benchApprovalsAt(b *testing.B, pending int) {
	handler, token := benchApprovalsGateway(b, pending)
	benchGET(b, handler, token, "/api/v1/approvals/pending")
}

// BenchmarkApprovalsPending25 is an ordinary workspace with a handful parked.
func BenchmarkApprovalsPending25(b *testing.B) { benchApprovalsAt(b, 25) }

// BenchmarkApprovalsPending200 is the query's own limit — the most the badge
// can ever be counting.
func BenchmarkApprovalsPending200(b *testing.B) { benchApprovalsAt(b, 200) }

// The same badge, served by the count endpoint. Compare against
// BenchmarkApprovalsPending* above: the number rendered is identical, so the
// difference is the whole cost of the rows the badge never looked at.
func benchApprovalsCountAt(b *testing.B, pending int) {
	handler, token := benchApprovalsGateway(b, pending)
	benchGET(b, handler, token, "/api/v1/approvals/pending/count")
}

func BenchmarkApprovalsCount25(b *testing.B)  { benchApprovalsCountAt(b, 25) }
func BenchmarkApprovalsCount200(b *testing.B) { benchApprovalsCountAt(b, 200) }

// benchRequestParallel is benchRequest under concurrency. The serial
// benchmarks above price one request; this one prices the ones a fleet
// serves at the same time, which is where a shared lock in the request
// path shows up and where a serial benchmark is blind by construction.
func benchRequestParallel(b *testing.B, method, path string) {
	handler, token := benchGateway(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(method, path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rw := &discardWriter{}
			handler.ServeHTTP(rw, req)
			if rw.status != http.StatusOK {
				b.Fatalf("%s %s = %d", method, path, rw.status)
			}
		}
	})
}

func BenchmarkGetMeParallel(b *testing.B) { benchRequestParallel(b, "GET", "/api/v1/me") }

// benchSaveFlowAt is the write path, which nothing here measured before: every
// benchmark above reads. The editor autosaves while a person is typing, so a
// save is not a rare event — it is the request most often in flight while
// someone works, and it validates, lints and commits the whole flow each time.
func benchSaveFlowAt(b *testing.B, steps int) {
	handler, token := benchGateway(b)
	g := core.Graph{
		ID: "bench", Tenant: "t", Workspace: "ws", Name: "Bench",
		Description: "a flow the size a real one is",
	}
	for i := range steps {
		g.Nodes = append(g.Nodes, core.Node{
			ID: fmt.Sprintf("n%d", i), Module: "http_request",
			Params: map[string]any{
				"url":    "https://api.example.com/v1/resource/" + fmt.Sprint(i),
				"method": "POST",
				"body": map[string]any{
					"note": "a realistic amount of configuration on every step",
					"idx":  i,
				},
			},
		})
		if i > 0 {
			g.Edges = append(g.Edges, core.Edge{
				From: fmt.Sprintf("n%d", i-1), FromPort: "pass",
				To: fmt.Sprintf("n%d", i), ToPort: "pass",
			})
		}
	}
	payload, err := json.Marshal(g)
	if err != nil {
		b.Fatalf("marshal: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest("PUT", "/api/v1/me/flows/t%2Fws%2Fbench", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rw := &discardWriter{}
		handler.ServeHTTP(rw, req)
		if rw.status != http.StatusOK {
			b.Fatalf("save = %d", rw.status)
		}
	}
}

func BenchmarkSaveFlow8(b *testing.B)  { benchSaveFlowAt(b, 8) }
func BenchmarkSaveFlow30(b *testing.B) { benchSaveFlowAt(b, 30) }
func BenchmarkSaveFlow60(b *testing.B) { benchSaveFlowAt(b, 60) }

// BenchmarkWebhookTrigger is the path an external system takes to fire a flow:
// POST /trigger/{tenant}/{workspace}/{flow}. It is a write (it submits a run)
// and the caller waits on it, and it was unbenchmarked — like every other
// write here. It loads the PUBLISHED flow from the workspace on every
// delivery, so its cost scales with the flow, not with the payload.
func benchWebhookTriggerAt(b *testing.B, steps int, wide bool) {
	// Over a real Postgres, not the in-memory store. The memory store's
	// Enqueue scans every record it holds to find the tenant's queue tail
	// (Postgres does it with an index), so a benchmark that submits a run per
	// iteration measures that scan growing — 96% of the profile — and nothing
	// about the handler.
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		b.Skip("set DAZYFLOW_TEST_DB to run the Postgres request benchmarks")
	}
	store, err := jobstore.OpenPostgres(context.Background(), url)
	if err != nil {
		b.Fatalf("OpenPostgres: %v", err)
	}
	b.Cleanup(store.Close)
	// Every iteration submits a run, so this benchmark writes rows to the
	// shared test database. Clear them at the end, or a later run of any
	// benchmark against the same database measures this one's leftovers.
	b.Cleanup(func() {
		if _, derr := store.DeleteByTenant(context.Background(), "t"); derr != nil {
			b.Logf("cleanup: %v", derr)
		}
	})
	// Before the gateway is built, not after: the listener takes log.Writer()
	// at construction, so muting the default logger later leaves it holding
	// the old one. It logs a line per delivery, which at benchmark rates is
	// both the dominant I/O and enough noise to make `go test -bench` output
	// unparseable by benchstat, which reads the same stream.
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(os.Stderr) })
	handler, token, _ := buildBenchGatewayWith(b, store)
	benchLastGateway.WebhookRateLimit = nil
	const secret = "whsec_bench_0123456789"

	g := core.Graph{
		ID: "hook", Tenant: "t", Workspace: "ws", Name: "Hook",
		Nodes: []core.Node{{
			ID: "in", Module: "webhook_input",
			Params: map[string]any{"secrets": []string{secret}},
		}},
	}
	for i := 1; i < steps; i++ {
		g.Nodes = append(g.Nodes, core.Node{
			ID: fmt.Sprintf("n%d", i), Module: "http_request",
			Params: map[string]any{
				"url":    "https://api.example.com/v1/resource/" + fmt.Sprint(i),
				"method": "POST",
				"body":   map[string]any{"note": "realistic step configuration", "idx": i},
			},
		})
		// chain: every step hangs off the one before, so a submit dispatches
		// exactly one root. wide: every step hangs off the trigger, so a submit
		// dispatches all of them at once. The two shapes cost very different
		// things at submit, and only the second shows it.
		from, fromPort := fmt.Sprintf("n%d", i-1), "pass"
		if i == 1 || wide {
			from, fromPort = "in", "body"
		}
		g.Edges = append(g.Edges, core.Edge{
			From: from, FromPort: fromPort,
			To: fmt.Sprintf("n%d", i), ToPort: "pass",
		})
	}
	payload, merr := json.Marshal(g)
	if merr != nil {
		b.Fatalf("marshal: %v", merr)
	}
	do := func(method, path string, body []byte, auth string) int {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		req.Header.Set("Content-Type", "application/json")
		rw := &discardWriter{}
		handler.ServeHTTP(rw, req)
		return rw.status
	}
	if code := do("PUT", "/api/v1/me/flows/t%2Fws%2Fhook", payload, "Bearer "+token); code != http.StatusOK {
		b.Fatalf("save = %d", code)
	}
	// Only the PUBLISHED revision fires, so publish before triggering.
	if code := do("POST", "/api/v1/me/flows/t%2Fws%2Fhook/publish", nil, "Bearer "+token); code != http.StatusOK {
		b.Fatalf("publish = %d", code)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if code := do("POST", "/trigger/t/ws/hook", []byte(`{"event":"bench"}`), "Bearer "+secret); code != http.StatusAccepted && code != http.StatusOK {
			b.Fatalf("trigger = %d", code)
		}
	}
}

func BenchmarkWebhookTrigger8(b *testing.B)      { benchWebhookTriggerAt(b, 8, false) }
func BenchmarkWebhookTrigger60(b *testing.B)     { benchWebhookTriggerAt(b, 60, false) }
func BenchmarkWebhookTriggerWide8(b *testing.B)  { benchWebhookTriggerAt(b, 8, true) }
func BenchmarkWebhookTriggerWide60(b *testing.B) { benchWebhookTriggerAt(b, 60, true) }
