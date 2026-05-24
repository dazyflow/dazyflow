// Command hzd is the Hazy Flow daemon. The step-4 build runs a single graph
// passed on the command line (or a baked-in demo when none is given) and
// prints the per-node results. Postgres-backed scheduling and the API
// surface come in later steps.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	_ "git.sr.ht/~klahr/hazy-flow/modules"
)

func main() {
	graphPath := flag.String("graph", "", "path to graph JSON; empty runs the demo")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	graph, err := loadGraph(*graphPath)
	if err != nil {
		log.Fatalf("load graph: %v", err)
	}

	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}

	progress := make(chan engine.GraphProgress, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for p := range progress {
			pct := "?"
			if p.Progress.Percent != nil {
				pct = fmt.Sprintf("%.0f%%", *p.Progress.Percent*100)
			}
			log.Printf("[%s] %s: %s", p.NodeID, pct, p.Progress.Message)
		}
	}()

	res, runErr := eng.Run(ctx, graph, progress)
	<-done

	if runErr != nil {
		log.Printf("graph failed: %v", runErr)
	}
	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		log.Fatalf("encode result: %v", err)
	}
	if runErr != nil {
		os.Exit(1)
	}
}

func loadGraph(path string) (core.Graph, error) {
	if path == "" {
		return demoGraph(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return core.Graph{}, fmt.Errorf("read %q: %w", path, err)
	}
	var g core.Graph
	if err := json.Unmarshal(data, &g); err != nil {
		return core.Graph{}, fmt.Errorf("parse %q: %w", path, err)
	}
	return g, nil
}

// demoGraph builds a small "sleep → sleep" pipeline so a fresh checkout can
// be tested with just `go run ./cmd/hzd`.
func demoGraph() core.Graph {
	return core.Graph{
		ID:      "demo",
		Version: "1",
		Nodes: []core.Node{
			{ID: "warmup", Module: "sleep", Params: map[string]any{"ms": 100}},
			{ID: "main", Module: "sleep", Params: map[string]any{"ms": 200}},
		},
		Edges: []core.Edge{
			{From: "warmup", FromPort: "out", To: "main", ToPort: "in"},
		},
	}
}
