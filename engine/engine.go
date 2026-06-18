package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"sync"

	"git.sr.ht/~klahr/hazyflow/core"
)

// GraphProgress wraps a node-level Progress event with the graph context so
// a single caller channel can multiplex progress from every node in the run.
type GraphProgress struct {
	JobID    string        `json:"job_id"`
	NodeID   string        `json:"node_id"`
	Progress core.Progress `json:"progress"`
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
	// Sandbox is optional. When set, every Job built by the engine has
	// its WorkspaceRoot populated from Sandbox.Root before Execute is
	// called. Filesystem-touching modules refuse to run without a root.
	Sandbox core.SandboxProvider
	// Quota is optional. When set, the engine snapshots the tenant's
	// byte budget onto each Job so modules can refuse writes that would
	// exceed it.
	Quota core.QuotaProvider
	// Secrets is a registry of secret providers keyed by URI scheme.
	// When set, the engine walks Job.Params/Env before each Execute and
	// replaces any "scheme://path" string with the provider's value.
	// Unresolved values stay in the JobStore so audit trails never
	// capture cleartext secrets.
	Secrets map[string]core.SecretProvider
	// Resources is an optional registry of resource providers keyed by
	// scheme ("resource"). When set, the engine resolves ${resource.NAME}
	// references in Job.Params to live external content (e.g. a Google
	// Sheet's rows) before Execute — a whole-string ref yields the
	// structured value, an inline one the stringified form. A fetch
	// failure fails the node with code "resource".
	Resources map[string]core.ResourceProvider
	// ApprovalSigner is optional. When set and the resolved module's
	// manifest has AwaitsApproval=true, the engine populates
	// Job.ApprovalURL pre-Execute so the module can emit the URL on
	// its output. Without a signer, awaiting-style modules still run
	// but receive an empty ApprovalURL.
	ApprovalSigner core.ApprovalSigner
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
	// Prefer a tenant-scoped manifest set so a graph validates against the
	// drops its tenant has installed (and any exact versions it pins), not the
	// global-default palette.
	if mp, ok := e.Resolver.(interface {
		ManifestsForTenant(string) map[string]core.Manifest
	}); ok {
		return core.ValidateWithManifests(graph, mp.ManifestsForTenant(graph.Tenant))
	}
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

// RunNode executes a single node using already-known predecessor results.
// It is the per-node counterpart to Run, used by the daemon's distributed
// worker so different nodes of a graph can land on different workers.
// progress receives core.Progress events directly (no GraphProgress
// wrapping) — the caller decides how to surface them.
//
// graphRunID identifies the specific run (not the persistent graph ID)
// and is used to scope the approval URL for await_approval modules.
// Callers that aren't running per-run can pass graph.ID.
func (e *Engine) RunNode(
	ctx context.Context,
	graph core.Graph,
	graphRunID string,
	nodeID string,
	recordID string,
	prior map[string]core.Result,
	progress chan<- core.Progress,
) (core.Result, error) {
	node, ok := graph.Node(nodeID)
	if !ok {
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "unknown_node", Message: fmt.Sprintf("node %q not in graph", nodeID)},
		}, fmt.Errorf("node %q not in graph", nodeID)
	}
	if e.Resolver == nil {
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "no_resolver", Message: "engine has no resolver"},
		}, fmt.Errorf("engine has no resolver")
	}
	ctx, span := startNodeSpan(ctx, graph, node)
	defer span.End()

	// Tenant rides on ctx through resolution so the scripted catalog returns
	// this tenant's installed (and version-pinned) drops, not the global set.
	ctx = core.WithTenant(ctx, graph.Tenant)

	transport, err := e.Resolver.Resolve(ctx, node.Module)
	if err != nil {
		recordSpanError(span, err)
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "resolve_failed", Message: err.Error()},
		}, err
	}
	manifest := transport.Manifest()
	input := assembleInput(graph, node.ID, manifest, prior)

	// The job ID is the idempotency key for outbound side effects
	// (Job.IdempotencyKey). It MUST be stable across retries so a retried
	// POST is deduped by the receiving service — the worker re-invokes
	// RunNode with the same record ID on every attempt. Fall back to a
	// random ID only for callers that don't supply one (e.g. ad-hoc tests).
	jobID := recordID
	if jobID == "" {
		jobID, err = newJobID()
		if err != nil {
			recordSpanError(span, err)
			return core.Result{Status: core.StatusError}, fmt.Errorf("generate job ID: %w", err)
		}
	}
	params, env := cloneNodeIO(node.Params, node.Env)
	job := core.Job{
		ID:      jobID,
		GraphID: graph.ID,
		NodeID:  node.ID,
		Input:   input,
		Params:  params,
		Env:     env,
		Cleanup: core.CleanupOnGraphComplete,
	}
	if err := e.populateSandbox(&job, graph, graphRunID); err != nil {
		recordSpanError(span, err)
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "sandbox", Message: err.Error()},
		}, err
	}
	sctx := scopeCtx(ctx, graph)
	injectConnectionDefaults(sctx, e.Secrets, manifest, &job)
	secrets, err := resolveTemplatesCollecting(sctx, e.Secrets, e.Resources, prior, &job)
	if err != nil {
		recordSpanError(span, err)
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: templateErrCode(err), Message: err.Error()},
		}, err
	}
	if manifest.AwaitsApproval && e.ApprovalSigner != nil {
		job.ApprovalURL = e.ApprovalSigner.SignApprovalURL(graphRunID, node.ID)
	}
	jobIDsFromSpan(ctx, &job)

	// Tenant rides on the context into Execute so connector token lookups
	// (OAuth GetOAuthToken) can resolve the per-tenant account.
	ctx = core.WithTenant(ctx, job.Tenant)
	ctx = WithResolver(ctx, e.Resolver)
	// Those same connector lookups resolve OAuth tokens *inside* Execute,
	// outside the secret-provider path that populated `secrets`. Expose a
	// sink so they register the resolved token for redaction too.
	ctx = withSecretSink(ctx, secrets)

	// Scrub secrets a drop might echo into live progress events before they
	// leave the engine — redactResult only covers the final Result.
	redactedProgress, progressDone := redactProgress(ctx, progress, secrets)
	result, execErr := transport.Execute(ctx, job, redactedProgress)
	if redactedProgress != nil {
		close(redactedProgress)
		<-progressDone
	}
	if result.JobID == "" {
		result.JobID = job.ID
	}
	core.ApplyPassthrough(job.Input, &result)
	// Scrub resolved secret values from the result before it leaves the
	// engine (and lands in the job store / run-detail UI).
	redactResult(&result, secrets)
	if execErr != nil {
		recordSpanError(span, execErr)
	} else if result.Status == core.StatusError && result.Error != nil {
		recordSpanError(span, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message))
	}
	return result, execErr
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

	// Tenant rides on ctx through resolution so the scripted catalog returns
	// this tenant's installed (and version-pinned) drops, not the global set.
	ctx = core.WithTenant(ctx, graph.Tenant)

	transport, err := e.Resolver.Resolve(ctx, node.Module)
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

	params, env := cloneNodeIO(node.Params, node.Env)
	job := core.Job{
		ID:      jobID,
		GraphID: graph.ID,
		NodeID:  node.ID,
		Input:   input,
		Params:  params,
		Env:     env,
		Cleanup: core.CleanupOnGraphComplete,
	}
	// The in-process Run path has no per-run ID of its own — but a loop-body
	// run carries the PARENT run's ID on ctx (WithLoopRunID) so body nodes
	// share the parent's scratch space. Outside a loop this stays "" (no
	// scratch), as before.
	if err := e.populateSandbox(&job, graph, loopRunIDFromContext(ctx)); err != nil {
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "sandbox", Message: err.Error()},
		}, err
	}
	sctx := scopeCtx(ctx, graph)
	injectConnectionDefaults(sctx, e.Secrets, transport.Manifest(), &job)
	secrets, err := resolveTemplatesCollecting(sctx, e.Secrets, e.Resources, prior, &job)
	if err != nil {
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: templateErrCode(err), Message: err.Error()},
		}, err
	}
	jobIDsFromSpan(ctx, &job)

	// Tenant rides on the context into Execute so connector token lookups
	// (OAuth GetOAuthToken) can resolve the per-tenant account.
	ctx = core.WithTenant(ctx, job.Tenant)
	ctx = WithResolver(ctx, e.Resolver)
	// Those same connector lookups resolve OAuth tokens *inside* Execute,
	// outside the secret-provider path that populated `secrets`. Expose a
	// sink so they register the resolved token for redaction too.
	ctx = withSecretSink(ctx, secrets)

	nodeProgress := make(chan core.Progress)
	forwarderDone := make(chan struct{})
	go forwardProgress(ctx, job.ID, node.ID, nodeProgress, progress, secrets, forwarderDone)

	result, execErr := transport.Execute(ctx, job, nodeProgress)
	close(nodeProgress)
	<-forwarderDone

	if result.JobID == "" {
		result.JobID = job.ID
	}
	core.ApplyPassthrough(job.Input, &result)
	// Scrub resolved secret values from the result before it leaves the
	// engine (and lands in the job store / run-detail UI).
	redactResult(&result, secrets)
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
		// Fallback edges never feed data into the downstream node — they
		// exist purely to trigger activation when the source fails. Any
		// data flow to this destination must come via separate edges.
		if edge.OnError == core.OnErrorFallback {
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
	set *secretSet,
	done chan<- struct{},
) {
	defer close(done)
	for p := range in {
		if out == nil {
			continue
		}
		p = redactProgressEvent(p, set)
		select {
		case out <- GraphProgress{JobID: jobID, NodeID: nodeID, Progress: p}:
		case <-ctx.Done():
			for range in {
			}
			return
		}
	}
}

// populateSandbox sets Job.WorkspaceRoot and snapshots quota state from
// the configured providers. Failures here short-circuit before
// transport.Execute so a misconfigured sandbox never lets a module run
// unsandboxed, and a quota-lookup error fails the job rather than
// silently allowing unmetered writes.
func (e *Engine) populateSandbox(job *core.Job, graph core.Graph, runID string) error {
	job.Tenant = graph.Tenant
	if e.Sandbox != nil {
		root, err := e.Sandbox.Root(graph.Tenant, graph.Workspace)
		if err != nil {
			return fmt.Errorf("sandbox for %s/%s: %w", graph.Tenant, graph.Workspace, err)
		}
		job.WorkspaceRoot = root
		// Per-run ephemeral scratch, when the provider supports it and we
		// have a run ID to namespace by. Reclaimed by the dispatcher when
		// the run finishes. The in-process Engine.Run path passes runID=""
		// (no scratch) — only the daemon's per-node RunNode path runs.
		if sp, ok := e.Sandbox.(core.ScratchProvider); ok && runID != "" {
			scratch, err := sp.ScratchRoot(graph.Tenant, graph.Workspace, runID)
			if err != nil {
				return fmt.Errorf("scratch for %s/%s run %s: %w", graph.Tenant, graph.Workspace, runID, err)
			}
			job.ScratchRoot = scratch
		}
	}
	if e.Quota != nil {
		limit := e.Quota.Limit(graph.Tenant)
		job.QuotaLimit = limit
		if limit > 0 {
			used, err := e.Quota.Used(graph.Tenant)
			if err != nil {
				return fmt.Errorf("quota lookup for %s: %w", graph.Tenant, err)
			}
			job.QuotaUsed = used
		}
	}
	return nil
}

func newJobID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// cloneNodeIO returns deep copies of a node's Params and Env. The engine
// resolves ${secret.…}/${upstream.…} placeholders into params IN PLACE, so
// without a copy the resolved cleartext (including secret values) would be
// written back into the caller's shared graph map — leaking secrets into any
// later serialization/inspection of the graph and making a re-run
// non-deterministic. Params can hold nested maps/slices, so a shallow
// maps.Clone is insufficient; a JSON round-trip mirrors how the graph is
// already stored and persisted. Env is flat strings, so a shallow clone is
// exact. On a (practically impossible) marshal error we fall back to the
// original maps rather than failing the node — resolution would then mutate
// in place, the pre-existing behavior.
func cloneNodeIO(params map[string]any, env map[string]string) (map[string]any, map[string]string) {
	outParams := params
	if len(params) > 0 {
		if b, err := json.Marshal(params); err == nil {
			var cp map[string]any
			if json.Unmarshal(b, &cp) == nil {
				outParams = cp
			}
		}
	}
	return outParams, maps.Clone(env)
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
