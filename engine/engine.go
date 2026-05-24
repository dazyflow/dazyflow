package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// GraphProgress wraps a node-level Progress event with the graph context so
// a single caller channel can multiplex progress from every node in the run.
type GraphProgress struct {
	JobID    string
	NodeID   string
	Progress core.Progress
}

// GraphResult is the final outcome of a graph run. Nodes contains the
// per-node Result for every node that completed (including the failing one
// when Status is "error"). Error is populated on engine- or node-level
// failure; the returned error wraps the same information.
type GraphResult struct {
	GraphID string
	Status  string
	Nodes   map[string]core.Result
	Error   *core.JobError
}

type Engine struct {
	Resolver Resolver
}

// Run validates the graph, computes parallel execution layers, and executes
// each layer concurrently. The first node failure aborts the run. If
// progress is non-nil the engine streams GraphProgress events on it and
// closes it on return.
func (e *Engine) Run(ctx context.Context, graph core.Graph, progress chan<- GraphProgress) (GraphResult, error) {
	ctx, span := startGraphSpan(ctx, graph)
	defer span.End()

	if progress != nil {
		defer close(progress)
	}
	if e.Resolver == nil {
		err := fmt.Errorf("engine has no resolver")
		recordSpanError(span, err)
		return errorResult(graph.ID, "no_resolver", err.Error()), err
	}

	if err := e.validate(graph); err != nil {
		recordSpanError(span, err)
		return errorResult(graph.ID, "invalid_graph", err.Error()), err
	}

	layers, err := core.ExecutionLayers(graph)
	if err != nil {
		recordSpanError(span, err)
		return errorResult(graph.ID, "invalid_graph", err.Error()), err
	}

	results := make(map[string]core.Result, len(graph.Nodes))

	for _, layer := range layers {
		if err := ctx.Err(); err != nil {
			recordSpanError(span, err)
			return cancelledResult(graph.ID, results, err), err
		}
		if err := e.runLayer(ctx, graph, layer, results, progress); err != nil {
			recordSpanError(span, err)
			return GraphResult{
				GraphID: graph.ID,
				Status:  core.StatusError,
				Nodes:   results,
				Error:   &core.JobError{Code: "node_failed", Message: err.Error()},
			}, err
		}
	}

	return GraphResult{GraphID: graph.ID, Status: core.StatusOK, Nodes: results}, nil
}

func (e *Engine) validate(graph core.Graph) error {
	if mp, ok := e.Resolver.(interface {
		Manifests() map[string]core.Manifest
	}); ok {
		return core.ValidateWithManifests(graph, mp.Manifests())
	}
	return core.Validate(graph)
}

// runLayer executes every node in a layer concurrently and merges their
// results into the shared map. Returns the first node error encountered; on
// success returns nil.
func (e *Engine) runLayer(
	ctx context.Context,
	graph core.Graph,
	layer []string,
	results map[string]core.Result,
	progress chan<- GraphProgress,
) error {
	type slot struct {
		id     string
		result core.Result
		err    error
	}
	out := make([]slot, len(layer))

	var wg sync.WaitGroup
	for i, nodeID := range layer {
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			node, _ := graph.Node(id)
			result, err := e.runNode(ctx, graph, node, results, progress)
			out[idx] = slot{id, result, err}
		}(i, nodeID)
	}
	wg.Wait()

	for _, s := range out {
		results[s.id] = s.result
		if s.err != nil {
			return fmt.Errorf("node %q: %w", s.id, s.err)
		}
		if s.result.Status == core.StatusError {
			msg := "unknown"
			if s.result.Error != nil {
				msg = s.result.Error.Message
			}
			return fmt.Errorf("node %q: %s", s.id, msg)
		}
	}
	return nil
}

// runNode resolves the transport, assembles the Job from upstream outputs,
// runs Execute with a per-node progress forwarder, and returns the result.
func (e *Engine) runNode(
	ctx context.Context,
	graph core.Graph,
	node core.Node,
	prior map[string]core.Result,
	progress chan<- GraphProgress,
) (core.Result, error) {
	ctx, span := startNodeSpan(ctx, graph, node)
	defer span.End()

	transport, err := e.Resolver.Resolve(node.Module)
	if err != nil {
		recordSpanError(span, err)
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "resolve_failed", Message: err.Error()},
		}, err
	}

	input := assembleInput(graph, node.ID, transport.Manifest(), prior)

	jobID, err := newJobID()
	if err != nil {
		recordSpanError(span, err)
		return core.Result{Status: core.StatusError}, fmt.Errorf("generate job ID: %w", err)
	}

	job := core.Job{
		ID:      jobID,
		GraphID: graph.ID,
		NodeID:  node.ID,
		Input:   input,
		Params:  node.Params,
		Env:     node.Env,
		Cleanup: core.CleanupOnGraphComplete,
	}
	jobIDsFromSpan(ctx, &job)

	nodeProgress := make(chan core.Progress)
	forwarderDone := make(chan struct{})
	go forwardProgress(ctx, job.ID, node.ID, nodeProgress, progress, forwarderDone)

	result, execErr := transport.Execute(ctx, job, nodeProgress)
	close(nodeProgress)
	<-forwarderDone

	if result.JobID == "" {
		result.JobID = job.ID
	}
	if execErr != nil {
		recordSpanError(span, execErr)
	} else if result.Status == core.StatusError && result.Error != nil {
		recordSpanError(span, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message))
	}
	return result, execErr
}

// assembleInput walks incoming edges and builds the Job.Input map by reading
// each upstream node's Result.Output. Variadic input ports get one entry per
// edge keyed as "port[idx]" — module authors recover the list with
// core.VariadicInputs. Non-variadic ports use the plain port name.
func assembleInput(graph core.Graph, nodeID string, manifest core.Manifest, prior map[string]core.Result) map[string]core.Ref {
	input := make(map[string]core.Ref)
	variadicCount := make(map[string]int)

	for _, edge := range graph.Edges {
		if edge.To != nodeID {
			continue
		}
		src, ok := prior[edge.From]
		if !ok || src.Output == nil {
			continue
		}
		ref, ok := src.Output[edge.FromPort]
		if !ok {
			continue
		}
		port, _ := manifest.Input(edge.ToPort)
		if port.Variadic {
			key := core.VariadicInputKey(edge.ToPort, variadicCount[edge.ToPort])
			input[key] = ref
			variadicCount[edge.ToPort]++
		} else {
			input[edge.ToPort] = ref
		}
	}
	return input
}

// forwardProgress drains the per-node channel, wrapping each event as a
// GraphProgress and sending it on the engine-wide channel. If ctx ends
// mid-run, we drop incoming events on the floor instead of forwarding —
// but we keep draining so Execute is never blocked on a full channel.
func forwardProgress(
	ctx context.Context,
	jobID, nodeID string,
	in <-chan core.Progress,
	out chan<- GraphProgress,
	done chan<- struct{},
) {
	defer close(done)
	for p := range in {
		if out == nil {
			continue
		}
		select {
		case out <- GraphProgress{JobID: jobID, NodeID: nodeID, Progress: p}:
		case <-ctx.Done():
			for range in {
			}
			return
		}
	}
}

func newJobID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func errorResult(graphID, code, msg string) GraphResult {
	return GraphResult{
		GraphID: graphID,
		Status:  core.StatusError,
		Error:   &core.JobError{Code: code, Message: msg},
	}
}

func cancelledResult(graphID string, results map[string]core.Result, err error) GraphResult {
	return GraphResult{
		GraphID: graphID,
		Status:  core.StatusError,
		Nodes:   results,
		Error:   &core.JobError{Code: "cancelled", Message: err.Error()},
	}
}
