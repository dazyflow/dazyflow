// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

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

func benchGateway(b *testing.B) (http.Handler, string) {
	b.Helper()
	return buildBenchGateway(b)
}

func benchGatewayT(t *testing.T) (http.Handler, string) { return buildBenchGateway(t) }

func buildBenchGateway(b testing.TB) (http.Handler, string) {
	h, tok, _ := buildBenchGatewayWith(b, jobstore.NewMemory())
	return h, tok
}

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

func BenchmarkGetMe(b *testing.B) { benchRequest(b, "GET", "/api/v1/me") }

func BenchmarkListDrops(b *testing.B) { benchRequest(b, "GET", "/api/v1/drops") }

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

func BenchmarkListDropsDiscard(b *testing.B) { benchRequestDiscard(b, "/api/v1/drops", "") }

func BenchmarkListDropsGzip(b *testing.B) { benchRequestDiscard(b, "/api/v1/drops", "gzip") }

func BenchmarkListDropsRevalidate(b *testing.B) {
	handler, token := benchGateway(b)
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

func BenchmarkRunList20(b *testing.B) { benchRunListAt(b, 20) }

func BenchmarkRunList200(b *testing.B) { benchRunListAt(b, 200) }

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

func BenchmarkGetRun(b *testing.B) {
	handler, token, runID := benchRunDetailGateway(b)
	benchGET(b, handler, token, "/api/v1/me/runs/"+runID)
}

func BenchmarkListRunNodes(b *testing.B) {
	handler, token, runID := benchRunDetailGateway(b)
	benchGET(b, handler, token, "/api/v1/me/runs/"+runID+"/nodes")
}

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

func BenchmarkApprovalsPending25(b *testing.B) { benchApprovalsAt(b, 25) }

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

func benchWebhookTriggerAt(b *testing.B, steps int, wide bool) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		b.Skip("set DAZYFLOW_TEST_DB to run the Postgres request benchmarks")
	}
	store, err := jobstore.OpenPostgres(context.Background(), url)
	if err != nil {
		b.Fatalf("OpenPostgres: %v", err)
	}
	b.Cleanup(store.Close)
	b.Cleanup(func() {
		if _, derr := store.DeleteByTenant(context.Background(), "t"); derr != nil {
			b.Logf("cleanup: %v", derr)
		}
	})
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
