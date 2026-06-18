// Command seed writes a single node-less poll-trigger graph into a
// git-backed workspace store, for the multi-node HA load test
// (scripts/ha_loadtest.sh).
//
// A node-less graph is deliberate: SubmitGraph accepts it and completes
// it immediately (daemon/seed.go), so every scheduler fire produces
// exactly one kind='graph' job row and nothing else. That makes the
// fired-count in Postgres a clean proxy for "how many times did a
// scheduler fire this trigger" — which is what the load test asserts.
package main

import (
	"flag"
	"log"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
)

func main() {
	base := flag.String("base", "", "workspace base dir (matches dzd --workspace-dir)")
	tenant := flag.String("tenant", "loadtest", "tenant")
	ws := flag.String("workspace", "default", "workspace")
	graphID := flag.String("graph", "ha-poll", "graph id")
	interval := flag.Int("interval", 1, "poll interval seconds")
	flag.Parse()

	if *base == "" {
		log.Fatal("seed: --base required")
	}

	g := core.Graph{
		ID:        *graphID,
		Version:   "1",
		Tenant:    *tenant,
		Workspace: *ws,
		// No nodes: fires and self-completes (one graph row per fire).
		Triggers: []core.GraphTrigger{{
			Type:            "poll",
			IntervalSeconds: *interval,
		}},
	}

	workspaces := daemon.NewAutoFSWorkspaces(*base)
	store, err := workspaces.Open(*tenant, *ws)
	if err != nil {
		log.Fatalf("seed: open workspace: %v", err)
	}
	if _, err := store.Save(g, "ha-loadtest"); err != nil {
		log.Fatalf("seed: save graph: %v", err)
	}
	log.Printf("seed: wrote %s/%s/%s (poll every %ds) under %s",
		*tenant, *ws, *graphID, *interval, *base)
}
