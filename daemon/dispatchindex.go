// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/dazyflow/dazyflow/core"
)

// graphTopology is a run's wiring in the shape the dispatcher asks about it:
// who depends on whom, which edges feed a given step, and which steps belong
// to a loop body. Derived from the graph, which is pinned for the life of a
// run, so it is computed once and reused.
type graphTopology struct {
	// outgoing maps a node to its dependents, deduplicated — parallel wires
	// between the same two steps are one dependent, not several.
	outgoing map[string][]string
	// incoming maps a node to the edges that feed it.
	incoming map[string][]core.Edge
	// bodyOwners maps a loop-body node to the for_each that owns it.
	bodyOwners map[string]string
}

func buildTopology(graph core.Graph) *graphTopology {
	t := &graphTopology{
		outgoing:   make(map[string][]string, len(graph.Nodes)),
		incoming:   make(map[string][]core.Edge, len(graph.Nodes)),
		bodyOwners: loopBodyOwners(graph),
	}
	seen := make(map[[2]string]struct{}, len(graph.Edges))
	for _, e := range graph.Edges {
		t.incoming[e.To] = append(t.incoming[e.To], e)
		pair := [2]string{e.From, e.To}
		if _, dup := seen[pair]; dup {
			continue
		}
		seen[pair] = struct{}{}
		t.outgoing[e.From] = append(t.outgoing[e.From], e.To)
	}
	return t
}

// maxCachedTopologies bounds the per-dispatcher cache. A worker advances a
// handful of runs at a time, and a topology is rebuilt on a miss, so a small
// window is enough to keep a dense graph from being re-indexed once per node.
const maxCachedTopologies = 8

type topologyCache struct {
	mu      sync.Mutex
	entries map[string]*graphTopology
	order   []string
}

func (c *topologyCache) get(runID string, graph core.Graph) *graphTopology {
	if runID == "" {
		return buildTopology(graph)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*graphTopology{}
	}
	if t, ok := c.entries[runID]; ok {
		return t
	}
	t := buildTopology(graph)
	c.entries[runID] = t
	c.order = append(c.order, runID)
	if len(c.order) > maxCachedTopologies {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
	return t
}

// dispatchIndex pairs a run's topology with the predecessor records read
// during one dispatch pass.
//
// It exists for cost. A dependent's readiness is a function of its incoming
// edges and their predecessors' records, and the dispatcher re-derived both
// by scanning the whole edge list and issuing one store read per edge —
// per dependent, after every node completion. On a densely wired flow that
// is O(nodes² × edges) with a store round trip per edge: 400 no-op steps
// took minutes, and against Postgres each of those reads is a query.
//
// A pass lasts milliseconds, so the cached records are as fresh as the
// point-reads they replace: a record that changes mid-pass can only make a
// dependent read as "not ready yet", and the completion that changed it
// runs its own pass. Records this pass writes itself are seeded via put.
type dispatchIndex struct {
	*graphTopology
	records map[string]core.JobRecord
	misses  map[string]struct{}
}

// indexFor builds a pass index over this run's cached topology.
func (d *Dispatcher) indexFor(graphRunID string, graph core.Graph) *dispatchIndex {
	return &dispatchIndex{
		graphTopology: d.topologies.get(graphRunID, graph),
		records:       map[string]core.JobRecord{},
		misses:        map[string]struct{}{},
	}
}

// pred returns the node record for nodeID in this run, reading it at most
// once per pass. A missing record is remembered too, so a step fed by many
// edges from the same not-yet-recorded predecessor costs one read.
func (ix *dispatchIndex) pred(ctx context.Context, store core.JobStore, graphRunID, nodeID string) (core.JobRecord, error) {
	if rec, ok := ix.records[nodeID]; ok {
		return rec, nil
	}
	if _, missing := ix.misses[nodeID]; missing {
		return core.JobRecord{}, core.ErrNotFound
	}
	rec, err := store.Get(ctx, NodeJobID(graphRunID, nodeID))
	if err != nil {
		ix.misses[nodeID] = struct{}{}
		return core.JobRecord{}, err
	}
	ix.records[nodeID] = rec
	return rec, nil
}

// put seeds a record this pass just wrote, so the cascade that follows sees
// it without a store read.
func (ix *dispatchIndex) put(rec core.JobRecord) {
	if rec.NodeID == "" {
		return
	}
	ix.records[rec.NodeID] = rec
	delete(ix.misses, rec.NodeID)
}

// debugDispatch turns on the per-dependent "waiting" trace, off by default:
// it is one line per dependent per completion, so a wide flow buries the
// log in it. Set DAZYFLOW_DEBUG_DISPATCH=1 when tracing why a step didn't
// run.
var debugDispatch = os.Getenv("DAZYFLOW_DEBUG_DISPATCH") != ""

// cachedRun is what a worker needs from a graph-record on every node of a
// run: the parsed graph and the run's own metadata.
type cachedRun struct {
	graph        core.Graph
	triggerDepth int
	// manual: a person started this run from the app and is watching it —
	// what a breakpoint needs to know (see shouldPauseAfter).
	manual bool
}

// RunCache holds that for recent runs. A run's payload is immutable once
// submitted, so the only staleness risk would be a caller mutating the
// returned graph — nothing on the run path does; every consumer reads it.
//
// Share one across a process's workers (WorkerConfig.Runs): a run's steps land
// on whichever worker is free, so per-worker caches each re-read the run
// record once — on a fleet of many workers that is a read per step.
//
// The window has to span every run the fleet has IN FLIGHT, not just the few
// a worker is advancing: under a backlog the queue interleaves orgs, so
// consecutive steps on one worker belong to different runs and a small window
// misses on every step. Each miss is a round trip AND a re-parse of the whole
// flow JSON. A window that large is only affordable because the parses are
// deduplicated — see graphs below.
type RunCache struct {
	mu      sync.Mutex
	max     int
	entries map[string]cachedRun
	order   []string

	// graphs interns parsed graphs by their payload bytes. A run pins its
	// flow definition at submit time, so every run of one flow carries a
	// byte-identical payload and they can share a single parse; different
	// bytes are a different flow, which is why the key is the payload and
	// not the (mutable) flow id. Without this the window above would hold
	// one parsed graph per in-flight run — hundreds of megabytes at a few
	// thousand runs — instead of one per distinct flow.
	graphs     map[string]core.Graph
	graphOrder []string
}

// maxCachedRuns is the default window when the caller does not size one. An
// entry is a graph header plus two fields, so this costs tens of kilobytes.
const maxCachedRuns = 4096

// maxInternedGraphs bounds the parse cache. Unlike a run entry this holds the
// payload (its key) and the decoded graph, so it is sized by distinct flows
// under load rather than by runs.
const maxInternedGraphs = 128

// NewRunCache bounds the cache at capacity runs; 0 means the default window.
func NewRunCache(capacity int) *RunCache {
	return &RunCache{max: capacity}
}

// graphFor decodes payload, reusing another run's decode of the same flow.
// The returned Graph shares its slices with every other holder, which is the
// same sharing the run window already relies on: the run path only reads a
// cached graph.
func (c *RunCache) graphFor(payload []byte) (core.Graph, error) {
	if c == nil {
		var g core.Graph
		return g, json.Unmarshal(payload, &g)
	}
	c.mu.Lock()
	// Keying a map with string(byteSlice) does not allocate — the compiler
	// hashes the bytes in place — so a hit costs one pass over the payload.
	if g, ok := c.graphs[string(payload)]; ok {
		c.mu.Unlock()
		return g, nil
	}
	c.mu.Unlock()

	var g core.Graph
	if err := json.Unmarshal(payload, &g); err != nil {
		return core.Graph{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.graphs == nil {
		c.graphs = map[string]core.Graph{}
	}
	key := string(payload)
	if _, dup := c.graphs[key]; !dup {
		c.graphs[key] = g
		c.graphOrder = append(c.graphOrder, key)
		if len(c.graphOrder) > maxInternedGraphs {
			delete(c.graphs, c.graphOrder[0])
			c.graphOrder = c.graphOrder[1:]
		}
	}
	return g, nil
}

// runFor is the cached read of a run's pinned flow: the parsed graph plus the
// run's own metadata, for a run id. A miss costs the record round trip and one
// decode; every later caller in this process — another worker's step, or a
// browser polling the run view every couple of seconds — is served from memory.
//
// A run's payload is immutable once submitted, so a hit can never be stale.
func (c *RunCache) runFor(ctx context.Context, store core.JobStore, graphRunID string) (cachedRun, error) {
	if c != nil {
		if run, ok := c.get(graphRunID); ok {
			return run, nil
		}
	}
	rec, err := loadRunRecord(ctx, store, graphRunID)
	if err != nil {
		return cachedRun{}, err
	}
	// Decode through the cache so concurrent runs of one flow share a parse.
	g, err := c.graphFor(rec.GraphPayload)
	if err != nil {
		return cachedRun{}, fmt.Errorf("unmarshal graph %s: %w", graphRunID, err)
	}
	run := cachedRun{graph: g, triggerDepth: rec.TriggerDepth, manual: rec.Manual}
	c.put(graphRunID, run)
	return run, nil
}

// graphForRun is runFor when only the flow is wanted.
func (c *RunCache) graphForRun(ctx context.Context, store core.JobStore, graphRunID string) (core.Graph, error) {
	run, err := c.runFor(ctx, store, graphRunID)
	return run.graph, err
}

func (c *RunCache) get(runID string) (cachedRun, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.entries[runID]
	return r, ok
}

func (c *RunCache) put(runID string, run cachedRun) {
	if c == nil || runID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedRun{}
	}
	if _, dup := c.entries[runID]; dup {
		return
	}
	c.entries[runID] = run
	c.order = append(c.order, runID)
	limit := c.max
	if limit <= 0 {
		limit = maxCachedRuns
	}
	if len(c.order) > limit {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
}

// runStateMeter accumulates how many bytes of step results a run has stored,
// so the worker can stop a run that is amplifying one payload across many
// steps (see resultStateBytes and core.MaxRunStateBytes).
//
// Per process, not per cluster: each worker process counts what it wrote, so
// with N dzd replicas the effective ceiling is N × the limit, and a run
// evicted from the window restarts its count. That is deliberate — this is a
// backstop against runaway amplification, not billing, and the alternative
// (an accumulator column read and written on every node) buys accuracy
// nobody needs at the cost of a round trip per step.
type runStateMeter struct {
	mu    sync.Mutex
	bytes map[string]int64
	order []string
}

// maxMeteredRuns bounds the meter's window. Generous next to how many runs a
// single worker has in flight.
const maxMeteredRuns = 512

// charge adds n bytes to runID's total and reports the new total plus
// whether the run is still inside the ceiling.
func (m *runStateMeter) charge(runID string, n int64) (int64, bool) {
	limit := int64(core.MaxRunStateBytes())
	if runID == "" || limit <= 0 {
		return 0, true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bytes == nil {
		m.bytes = map[string]int64{}
	}
	if _, seen := m.bytes[runID]; !seen {
		m.order = append(m.order, runID)
		if len(m.order) > maxMeteredRuns {
			delete(m.bytes, m.order[0])
			m.order = m.order[1:]
		}
	}
	m.bytes[runID] += n
	total := m.bytes[runID]
	return total, total <= limit
}

// resultStateBytes approximates what storing this result costs. Measured
// with the per-value ceiling as the budget, so an oversized value (already
// refused by the engine) can't make the measurement itself expensive.
func resultStateBytes(r *core.Result) int64 {
	if r == nil {
		return 0
	}
	budget := core.MaxValueBytes()
	var total int64
	for _, ref := range r.Output {
		total += int64(core.ApproxValueSize(ref.Inline, budget))
	}
	return total
}

// RunCache is the one cache of parsed run payloads a process keeps. The
// workers and the HTTP read paths share it: a run's steps and the browser
// watching them ask the same question, and the answer cannot change.
func (s *Service) RunCache() *RunCache {
	s.runsOnce.Do(func() { s.runsCache = NewRunCache(0) })
	return s.runsCache
}
