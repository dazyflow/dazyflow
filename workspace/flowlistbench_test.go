package workspace

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
)

// benchPgSeq keeps each benchmark's workspace to itself, so a rerun does not
// list a previous one's flows.
var benchPgSeq atomic.Int64

func benchPgWorkspace(tb testing.TB) *Store {
	tb.Helper()
	dsn := os.Getenv("DAZYFLOW_TEST_DB")
	if dsn == "" {
		tb.Skip("set DAZYFLOW_TEST_DB to run the Postgres workspace benchmarks")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		tb.Fatalf("pgxpool: %v", err)
	}
	tb.Cleanup(pool.Close)
	if err := EnsurePgWorkspaceSchema(ctx, pool); err != nil {
		tb.Fatalf("schema: %v", err)
	}
	s, err := OpenPostgres(pool, fmt.Sprintf("bench-%d", benchPgSeq.Add(1)), "main")
	if err != nil {
		tb.Fatalf("OpenPostgres: %v", err)
	}
	return s
}

func seedFlows(tb testing.TB, s *Store, flows, steps int) *Store {
	tb.Helper()
	for f := range flows {
		g := core.Graph{
			ID: fmt.Sprintf("flow-%02d", f), Name: fmt.Sprintf("Flow %d", f),
			Description: "a flow that does a realistic amount of work",
		}
		for i := range steps {
			g.Nodes = append(g.Nodes, core.Node{
				ID: fmt.Sprintf("n%d", i), Module: "http_request",
				Params: map[string]any{
					"url":    "https://api.example.com/v1/resource/" + fmt.Sprint(i),
					"method": "POST",
					"body": map[string]any{
						"note": "a realistic amount of configuration on every step, so the stored flow is the size a real one is",
						"idx":  i,
					},
				},
			})
		}
		if _, err := s.Save(g, "bench"); err != nil {
			tb.Fatalf("save %d: %v", f, err)
		}
	}
	return s
}

func benchWorkspace(tb testing.TB, flows, steps int) *Store {
	tb.Helper()
	s, err := OpenFS(tb.TempDir())
	if err != nil {
		tb.Fatalf("OpenFS: %v", err)
	}
	return seedFlows(tb, s, flows, steps)
}

// BenchmarkFlowListPgLoad and BenchmarkFlowListPgAtHead are the same pair
// against the Postgres graph store, where the per-flow shape costs three
// round trips per flow rather than a repeated tree walk.
func BenchmarkFlowListPgLoad(b *testing.B) {
	s := seedFlows(b, benchPgWorkspace(b), 50, 30)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ids, err := s.ListGraphs()
		if err != nil {
			b.Fatalf("list: %v", err)
		}
		for _, id := range ids {
			g, err := s.Load(id)
			if err != nil {
				b.Fatalf("load %s: %v", id, err)
			}
			_ = g.Name
			if _, err := s.PublishedCommit(id); err != nil {
				b.Fatalf("published %s: %v", id, err)
			}
		}
	}
}

func BenchmarkFlowListPgAtHead(b *testing.B) {
	s := seedFlows(b, benchPgWorkspace(b), 50, 30)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got, err := s.ListAtHead(PublishedEnv)
		if err != nil {
			b.Fatalf("list: %v", err)
		}
		if len(got) != 50 {
			b.Fatalf("got %d flows", len(got))
		}
		for _, f := range got {
			_ = f.Graph.Name
		}
	}
}

// BenchmarkFlowListAtHead is the same list through the one-pass read.
func BenchmarkFlowListAtHead(b *testing.B) {
	for _, flows := range []int{10, 50} {
		b.Run(fmt.Sprintf("flows%d", flows), func(b *testing.B) {
			s := benchWorkspace(b, flows, 30)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				got, err := s.ListAtHead(PublishedEnv)
				if err != nil {
					b.Fatalf("list: %v", err)
				}
				if len(got) != flows {
					b.Fatalf("got %d flows, want %d", len(got), flows)
				}
				for _, f := range got {
					_ = f.Graph.Name
				}
			}
		})
	}
}

// BenchmarkFlowListLoad is what ListFlowSummaries did: list the ids, then
// load each flow whole and read a name off it.
func BenchmarkFlowListLoad(b *testing.B) {
	for _, flows := range []int{10, 50} {
		b.Run(fmt.Sprintf("flows%d", flows), func(b *testing.B) {
			s := benchWorkspace(b, flows, 30)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				ids, err := s.ListGraphs()
				if err != nil {
					b.Fatalf("list: %v", err)
				}
				for _, id := range ids {
					g, err := s.Load(id)
					if err != nil {
						b.Fatalf("load %s: %v", id, err)
					}
					_ = g.Name
					if _, err := s.PublishedCommit(id); err != nil {
						b.Fatalf("published %s: %v", id, err)
					}
				}
			}
		})
	}
}
