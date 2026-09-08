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

func BenchmarkFlowListHeadOnly(b *testing.B) {
	s := benchWorkspace(b, 30, 25)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Head(); err != nil {
			b.Fatalf("head: %v", err)
		}
	}
}

func BenchmarkFlowListHeadersAtHead30(b *testing.B) {
	s := benchWorkspace(b, 30, 25)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got, err := s.ListHeadersAtHead("")
		if err != nil {
			b.Fatalf("list: %v", err)
		}
		if len(got) != 30 {
			b.Fatalf("got %d flows", len(got))
		}
	}
}

func BenchmarkFlowListAtHead30(b *testing.B) {
	s := benchWorkspace(b, 30, 25)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got, err := s.ListAtHead("")
		if err != nil {
			b.Fatalf("list: %v", err)
		}
		if len(got) != 30 {
			b.Fatalf("got %d flows", len(got))
		}
	}
}

// The one that sees the defect the serial benchmarks above cannot: every
// reader of a workspace serializes on one mutex, so what matters is how much
// of the read is held under it, not how long the read takes alone. Run it
// across -cpu to watch the ceiling.
func BenchmarkFlowListHeadersParallel(b *testing.B) {
	s := benchWorkspace(b, 30, 25)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			got, err := s.ListHeadersAtHead("")
			if err != nil {
				b.Fatalf("list: %v", err)
			}
			if len(got) != 30 {
				b.Fatalf("got %d flows", len(got))
			}
		}
	})
}

// The single-flow read — opening a flow in the editor, and LoadPublished on
// every trigger fire — under the contention the serial benchmarks cannot show.
// It shares the workspace mutex with every list above, so what it holds under
// the lock bounds them too.
func BenchmarkFlowLoadParallel(b *testing.B) {
	s := benchWorkspace(b, 30, 25)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			g, err := s.Load(fmt.Sprintf("flow-%02d", i%30))
			if err != nil {
				b.Fatalf("load: %v", err)
			}
			if len(g.Nodes) != 25 {
				b.Fatalf("got %d nodes", len(g.Nodes))
			}
			i++
		}
	})
}
