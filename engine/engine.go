// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"sync"

	"go.opentelemetry.io/otel/trace"

	"github.com/dazyflow/dazyflow/core"
)

type GraphProgress struct {
	JobID    string        `json:"job_id"`
	NodeID   string        `json:"node_id"`
	Progress core.Progress `json:"progress"`
}

type GraphResult struct {
	GraphID string
	Status  string
	Nodes   map[string]core.Result
	Error   *core.JobError
}

type Engine struct {
	Resolver Resolver
	Sandbox  core.SandboxProvider
	// Quota snapshots the tenant's byte budget onto each Job, so modules can
	// refuse writes that would exceed it.
	Quota core.QuotaProvider
	// Secrets resolves "scheme://path" strings in Params and Env before each
	// Execute. The UNresolved values are what reach the JobStore, so audit
	// trails never capture cleartext.
	Secrets        map[string]core.SecretProvider
	Resources      map[string]core.ResourceProvider
	EmailTemplates EmailTemplateProvider
	ApprovalSigner core.ApprovalSigner
	WriteDedupe    core.WriteDedupeStore
}

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

	// Merge every sibling BEFORE inspecting any for failure. One pass returned
	// on the first bad slot, dropping the nodes ordered after it — and since
	// ExecutionLayers sorts by node ID, which siblings survived was alphabetical
	// accident. They all ran, so the caller should see them all:
	// Worker.bodyRunner drives loop bodies through Run and reads
	// GraphResult.Nodes, so a body with one failing node lost its siblings'
	// output.
	for _, s := range out {
		results[s.id] = s.result
	}
	for _, s := range out {
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

	// The job ID is the idempotency key for outbound side effects, so it MUST be
	// stable across retries — the worker re-invokes RunNode with the same record
	// ID every attempt. Only a caller supplying none gets a random one.
	jobID := recordID
	if jobID == "" {
		id, err := newJobID()
		if err != nil {
			recordSpanError(span, err)
			return core.Result{Status: core.StatusError}, fmt.Errorf("generate job ID: %w", err)
		}
		jobID = id
	}

	return e.buildAndExecute(ctx, span, graph, node, prior, jobID, graphRunID,
		func(err error) { recordSpanError(span, err) },
		func(execCtx context.Context, transport core.Transport, job core.Job, secrets *secretSet) (core.Result, error) {
			// redactResult covers only the final Result, so scrub live progress
			// events a drop might echo a secret into.
			redactedProgress, progressDone := redactProgress(execCtx, progress, secrets)
			// Deferred, not inline: a panic out of Execute must still close the
			// channel and drain the forwarder. Native drops recover inside their
			// transport but the remote one does not, so an inline close leaked the
			// redaction goroutine for the life of the process whenever a gRPC node
			// server misbehaved. The defer still runs before control returns, so
			// the forwarding order is unchanged.
			defer func() {
				if redactedProgress != nil {
					close(redactedProgress)
					<-progressDone
				}
			}()
			return transport.Execute(execCtx, job, redactedProgress)
		})
}

func (e *Engine) runNode(
	ctx context.Context,
	graph core.Graph,
	node core.Node,
	prior map[string]core.Result,
	progress chan<- GraphProgress,
) (core.Result, error) {
	ctx, span := startNodeSpan(ctx, graph, node)
	defer span.End()

	jobID, err := newJobID()
	if err != nil {
		return core.Result{Status: core.StatusError}, fmt.Errorf("generate job ID: %w", err)
	}

	// The in-process path has no run ID of its own, but a loop body carries the
	// PARENT run's on ctx so body nodes share its scratch. Outside a loop this
	// stays "", meaning no scratch. populateSandbox reads it off ctx, so it must
	// run inside buildAndExecute.
	//
	// recordErr is a no-op: this path never stamped the span with sandbox or
	// template errors, and changing that would change emitted spans.
	return e.buildAndExecute(ctx, span, graph, node, prior, jobID,
		loopRunIDFromContext(ctx),
		func(error) {},
		func(execCtx context.Context, transport core.Transport, job core.Job, secrets *secretSet) (core.Result, error) {
			nodeProgress := make(chan core.Progress)
			forwarderDone := make(chan struct{})
			go forwardProgress(execCtx, job.ID, node.ID, nodeProgress, progress, secrets, forwarderDone)
			result, execErr := transport.Execute(execCtx, job, nodeProgress)
			close(nodeProgress)
			<-forwarderDone
			return result, execErr
		})
}

func (e *Engine) buildAndExecute(
	ctx context.Context,
	span trace.Span,
	graph core.Graph,
	node core.Node,
	prior map[string]core.Result,
	jobID, scratchRunID string,
	recordErr func(error),
	exec func(ctx context.Context, transport core.Transport, job core.Job, secrets *secretSet) (core.Result, error),
) (core.Result, error) {
	ctx = core.WithTenant(ctx, graph.Tenant)

	transport, err := e.Resolver.Resolve(ctx, node.Module)
	if err != nil {
		recordSpanError(span, err)
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "resolve_failed", Message: err.Error()},
		}, err
	}
	manifest := core.MarkListPorts(transport.Manifest())
	input := AssembleInput(graph, node.ID, manifest, prior)
	// InlineOnly was advisory until now — only a tooltip read it — so
	// run_on_runner silently ran its script with EMPTY stdin when a
	// file-producing step was wired in, exited 0, and reported SUCCESS. Enforced
	// here rather than in a transport because the declaring drop may be native,
	// and core/manifest.go promises the job is refused before dispatch.
	if err := refuseInlineOnlyFileRefs(manifest, input); err != nil {
		recordErr(err)
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "file_input_unsupported", Message: err.Error()},
		}, err
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
	if err := e.populateSandbox(&job, graph, scratchRunID); err != nil {
		recordErr(err)
		return core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "sandbox", Message: err.Error()},
		}, err
	}
	sctx := scopeCtx(ctx, graph)
	injectConnectionDefaults(sctx, e.Secrets, manifest, &job)
	secrets, err := resolveTemplatesCollecting(sctx, e.Secrets, e.Resources, graph, prior, &job)
	if err != nil {
		recordErr(err)
		// The error string can embed an already-resolved secret — one spliced
		// into a DSN that then fails to parse — and this is the only early return
		// that can, the paths above running before anything is resolved. So scrub
		// it with the redactor the success path uses.
		res := core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: templateErrCode(err), Message: err.Error()},
		}
		redactResult(&res, secrets)
		return res, err
	}
	if manifest.AwaitsApproval && e.ApprovalSigner != nil {
		job.ApprovalURL = e.ApprovalSigner.SignApprovalURL(scratchRunID, node.ID)
	}
	jobIDsFromSpan(ctx, &job)

	// Tenant rides into Execute so connector token lookups resolve the
	// per-tenant account.
	ctx = core.WithTenant(ctx, job.Tenant)
	ctx = WithResolver(ctx, e.Resolver)
	ctx = WithEmailTemplateProvider(ctx, e.EmailTemplates)
	ctx = withSecretSink(ctx, secrets)

	// Many→one: a single-value input handed a list runs the node once per item
	// and aggregates, falling straight through to one exec without fan-out.
	//
	// Dedupe is applied PER EXECUTION, not per node: when a list fans this node,
	// runMaybeFanned calls exec once per item in a stable order, so keying each
	// as job.ID#<idx> dedupes every send independently. A crash after sending
	// items 0..2 of 5 replays only 3..4 on reclaim, where a whole-node key would
	// re-send 0..2 — the very double-fire DedupeWrites prevents. A non-fanned
	// node is the one-item case. idx advances from a single goroutine, so it
	// needs no lock.
	if manifest.DedupeWrites && e.WriteDedupe != nil && job.ID != "" {
		store := e.WriteDedupe
		baseExec := exec
		idx := 0
		exec = func(ctx context.Context, transport core.Transport, j core.Job, secrets *secretSet) (core.Result, error) {
			key := fmt.Sprintf("%s#%d", job.ID, idx)
			idx++
			if prior, ok := store.Get(ctx, key); ok {
				return prior, nil
			}
			res, err := baseExec(ctx, transport, j, secrets)
			if err == nil && res.Status == core.StatusOK {
				// The side effect already succeeded, so a lost lease cancelling ctx
				// must not suppress the record and let a reclaim re-fire it — but a
				// shared store must not block the worker forever either.
				// WithoutCancel strips the deadline too, so re-impose a budget.
				putCtx, cancelPut := context.WithTimeout(context.WithoutCancel(ctx), dedupePutTimeout)
				store.Put(putCtx, key, res)
				cancelPut()
			}
			return res, err
		}
	}
	result, execErr := runMaybeFanned(ctx, manifest, job, secrets, transport, exec)

	if result.JobID == "" {
		result.JobID = job.ID
	}
	// Checked before the value reaches the job store or the next step. An
	// uncapped value compounds — a step referencing its predecessor twice
	// doubles it — and the eventual out-of-memory throw no recover catches takes
	// the whole daemon down rather than this one node.
	if oversize := oversizedOutput(result.Output); oversize != nil {
		result = core.Result{
			JobID:  result.JobID,
			Status: core.StatusError,
			Error:  &core.JobError{Code: "value_too_large", Message: oversize.Error()},
		}
	}
	core.ApplyPassthrough(job.Input, &result)
	redactResult(&result, secrets)
	if execErr != nil {
		recordSpanError(span, execErr)
	} else if result.Status == core.StatusError && result.Error != nil {
		recordSpanError(span, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message))
	}
	return result, execErr
}

func oversizedOutput(out map[string]core.Ref) *ValueTooLargeError {
	ports := make([]string, 0, len(out))
	for port := range out {
		ports = append(ports, port)
	}
	sort.Strings(ports) // stable message when several ports are oversized
	for _, port := range ports {
		if size, too := core.RefTooLarge(out[port]); too {
			return &ValueTooLargeError{
				What:  fmt.Sprintf("output %q", port),
				Size:  size,
				Limit: core.MaxValueBytes(),
			}
		}
	}
	return nil
}

// AssembleInput builds Job.Input from each upstream node's Result.Output.
// Variadic ports get one entry per edge keyed "port[idx]", which module authors
// recover with core.VariadicInputs.
//
// Exported because it is also how a run is EXPLAINED afterwards: a node record
// stores what a node produced, never what it received, so the run viewer has to
// rebuild the inputs from the run's own graph and stored outputs. Calling this
// rather than re-deriving "the upstream output for each edge" keeps the
// explanation honest — variadic fan-in, dataless fallback edges and the
// one→many auto-lift are all decided here, and a second implementation would
// quietly disagree.
func AssembleInput(graph core.Graph, nodeID string, manifest core.Manifest, prior map[string]core.Result) map[string]core.Ref {
	input := make(map[string]core.Ref)
	variadicCount := make(map[string]int)

	for _, edge := range graph.Edges {
		if edge.To != nodeID {
			continue
		}
		// Fallback edges only trigger activation when the source fails; data
		// must reach the destination through separate edges.
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
			input[edge.ToPort] = autoLiftToList(port, ref)
		}
	}
	return input
}

func autoLiftToList(port core.Port, ref core.Ref) core.Ref {
	if !port.List || ref.Inline == nil {
		return ref
	}
	if k := reflect.TypeOf(ref.Inline).Kind(); k == reflect.Slice || k == reflect.Array {
		return ref
	}
	ref.Inline = []any{ref.Inline}
	return ref
}

// forwardProgress keeps draining after ctx ends, dropping events rather than
// forwarding them, so Execute is never blocked on a full channel.
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

// populateSandbox short-circuits before transport.Execute, so a misconfigured
// sandbox never lets a module run unsandboxed and a quota-lookup error fails the
// job rather than silently allowing unmetered writes.
func (e *Engine) populateSandbox(job *core.Job, graph core.Graph, runID string) error {
	job.Tenant = graph.Tenant
	job.Language = graph.Language
	if e.Sandbox != nil {
		root, err := e.Sandbox.Root(graph.Tenant, graph.Workspace)
		if err != nil {
			return fmt.Errorf("sandbox for %s/%s: %w", graph.Tenant, graph.Workspace, err)
		}
		job.WorkspaceRoot = root
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
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// cloneNodeIO deep-copies Params and Env. The engine resolves placeholders IN
// PLACE, so without a copy the resolved cleartext would be written back into the
// caller's shared graph map — leaking secrets into any later serialization and
// making a re-run non-deterministic. Params can nest, so a shallow maps.Clone is
// insufficient and a JSON round-trip mirrors how the graph is already stored;
// Env is flat strings, so a shallow clone is exact. A marshal error falls back to
// the originals rather than failing the node.
func cloneNodeIO(params map[string]any, env map[string]string) (map[string]any, map[string]string) {
	// The graph these params come from is shared by every step of the run and,
	// through the worker's run cache, by every concurrent run of the same flow.
	// Resolution writes into what it gets back, so a shared map would leak one
	// run's resolved values into another's.
	outParams := maps.Clone(params)
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
