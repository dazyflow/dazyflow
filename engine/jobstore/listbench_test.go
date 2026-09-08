// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func benchGraphPayload(tb testing.TB, n int) []byte {
	tb.Helper()
	g := core.Graph{ID: "flow-bench", Tenant: "t", Workspace: "ws"}
	for i := range n {
		g.Nodes = append(g.Nodes, core.Node{
			ID:     fmt.Sprintf("n%d", i),
			Module: "http_request",
			Params: map[string]any{
				"url":     "https://api.example.com/v1/resource/" + fmt.Sprint(i),
				"method":  "POST",
				"headers": map[string]any{"content-type": "application/json", "x-trace": "abcdef0123456789"},
				"body": map[string]any{
					"note": "a realistic amount of configuration on every step, so the stored payload is the size a real flow's is",
					"idx":  i,
				},
			},
		})
		if i > 0 {
			g.Edges = append(g.Edges, core.Edge{
				From: fmt.Sprintf("n%d", i-1), FromPort: "body",
				To: fmt.Sprintf("n%d", i), ToPort: "body",
			})
		}
	}
	b, err := json.Marshal(g)
	if err != nil {
		tb.Fatalf("marshal graph: %v", err)
	}
	return b
}

func benchStore(tb testing.TB) *Postgres {
	tb.Helper()
	base := os.Getenv("DAZYFLOW_TEST_DB")
	if base == "" {
		tb.Skip("set DAZYFLOW_TEST_DB to run Postgres benchmarks")
	}
	dsn, err := ownDatabase(base)
	if err != nil {
		tb.Fatalf("provision database: %v", err)
	}
	store, err := OpenPostgres(context.Background(), dsn)
	if err != nil {
		tb.Fatalf("OpenPostgres: %v", err)
	}
	tb.Cleanup(store.Close)
	return store
}

func benchTenant(tb testing.TB, s *Postgres, steps, runs int) string {
	tb.Helper()
	ctx := context.Background()
	tenant := fmt.Sprintf("bench-%dstep", steps)
	var have int
	_ = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM jobs WHERE kind='graph' AND tenant=$1`, tenant).Scan(&have)
	if have >= runs {
		return tenant
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM jobs WHERE tenant=$1`, tenant); err != nil {
		tb.Fatalf("clear: %v", err)
	}
	payload := benchGraphPayload(tb, steps)
	tb.Logf("%s: %d-step flow, %d bytes of payload per run", tenant, steps, len(payload))
	res := &core.Result{Status: core.StatusError,
		Error: &core.JobError{Code: "http_status", Message: "502 from upstream"}}
	base := time.Now().Add(-time.Duration(runs) * time.Second)
	for i := range runs {
		st := core.JobStatusSucceeded
		if i%7 == 0 {
			st = core.JobStatusFailed
		}
		if i < 5 {
			st = core.JobStatusRunning
		}
		rec := core.JobRecord{
			ID: fmt.Sprintf("%s-run-%06d", tenant, i), Kind: core.JobKindGraph,
			GraphID: "flow-bench", Tenant: tenant, Workspace: "ws",
			Status: st, GraphPayload: payload,
			EnqueuedAt: base.Add(time.Duration(i) * time.Second),
		}
		if st == core.JobStatusFailed {
			rec.Result = res
		}
		if err := s.Enqueue(ctx, rec); err != nil {
			tb.Fatalf("enqueue %d: %v", i, err)
		}
	}
	if _, err := s.pool.Exec(ctx, `ANALYZE jobs`); err != nil {
		tb.Fatalf("analyze: %v", err)
	}
	return tenant
}

var benchRunSizes = []int{12, 100}

func BenchmarkRunListFull(b *testing.B) {
	s := benchStore(b)
	ctx := b.Context()
	for _, steps := range benchRunSizes {
		tenant := benchTenant(b, s, steps, 2000)
		for _, limit := range []int{20, 50, 200} {
			b.Run(fmt.Sprintf("steps%d/limit%d", steps, limit), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					recs, err := s.ListGraphRuns(ctx, core.ListGraphRunsOpts{
						Tenant: tenant, Workspace: "ws", Limit: limit,
					})
					if err != nil {
						b.Fatalf("list: %v", err)
					}
					if len(recs) != limit {
						b.Fatalf("got %d rows, want %d", len(recs), limit)
					}
				}
			})
		}
	}
}

func BenchmarkRunListSummary(b *testing.B) {
	s := benchStore(b)
	ctx := b.Context()
	for _, steps := range benchRunSizes {
		tenant := benchTenant(b, s, steps, 2000)
		for _, limit := range []int{20, 50, 200} {
			b.Run(fmt.Sprintf("steps%d/limit%d", steps, limit), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					recs, err := s.ListGraphRunSummaries(ctx, core.ListGraphRunsOpts{
						Tenant: tenant, Workspace: "ws", Limit: limit,
					})
					if err != nil {
						b.Fatalf("list: %v", err)
					}
					if len(recs) != limit {
						b.Fatalf("got %d rows, want %d", len(recs), limit)
					}
				}
			})
		}
	}
}

func BenchmarkAdmissionCountFull(b *testing.B) {
	s := benchStore(b)
	ctx := b.Context()
	tenant := benchTenant(b, s, 100, 2000)
	b.ReportAllocs()
	for b.Loop() {
		recs, err := s.ListGraphRuns(ctx, core.ListGraphRunsOpts{
			Tenant: tenant, Status: core.JobStatusRunning, Limit: 200,
		})
		if err != nil {
			b.Fatalf("list: %v", err)
		}
		_ = len(recs)
	}
}

func BenchmarkAdmissionCount(b *testing.B) {
	s := benchStore(b)
	ctx := b.Context()
	tenant := benchTenant(b, s, 100, 2000)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.CountGraphRuns(ctx, core.ListGraphRunsOpts{
			Tenant: tenant, Status: core.JobStatusRunning, Limit: 200,
		}); err != nil {
			b.Fatalf("count: %v", err)
		}
	}
}
