package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/limits"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "for_each",
			Version:     "1.0",
			Label:       "For each",
			Icon:        "repeat",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"iterate", "loop", "fan_out", "map"},
			Description: "Run a configured step module once per item in an input list. Items execute in parallel up to params.concurrency. Outputs `results` (one Result per item, in order) and `errors` (a map of failing indices). Set fail_fast=true to abort on the first failure; otherwise the iteration continues and per-item errors surface on the errors port.",
			Summary:     "Fan out a list and run the same step module on every item, optionally in parallel, collecting results in order.",
			Examples: []core.ParamsExample{
				{
					Title:  "POST each row to a webhook, 5 at a time",
					Params: json.RawMessage(`{"step_module":"http","step_params":{"method":"POST","url":"https://api.example.com/orders","body":"${item.}"},"concurrency":5}`),
					Notes:  "${item.} splices the whole item as JSON into the body. Use ${item.field.subfield} for a nested value.",
				},
				{
					Title:  "Send a templated email per recipient, stop on first failure",
					Params: json.RawMessage(`{"step_module":"email_send","step_params":{"to":"${item.email}","subject":"Hi ${item.name}","body":"Welcome aboard."},"fail_fast":true}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:  "items",
				Label: "List to iterate (Inline = []any or []core.Ref)",
			}},
			Outputs: []core.Port{
				{Port: "results", Label: "Per-item Result list", MIME: []string{MIMEList}},
				{Port: "errors", Label: "Failures keyed by item index", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"step_module":{"type":"string"},
						"step_params":{"type":"object"},
						"item_port":{"type":"string","default":"in"},
						"concurrency":{"type":"integer","minimum":1},
						"fail_fast":{"type":"boolean","default":false}
					},
					"required":["step_module"]
				}`,
			),
			Idempotent: true,
		},
		Execute: executeForEach,
	})
}

// resolveStep resolves the step module through the engine's full resolver
// (native + scripted + remote + MCP) when the engine put it on the context —
// so a scripted/marketplace drop can be a for_each step, not just a native one.
// Falls back to the native registry for callers that don't set a resolver.
func resolveStep(ctx context.Context, moduleID string) (core.Transport, bool) {
	if r, ok := engine.ResolverFromContext(ctx); ok {
		// ctx already carries the tenant (the engine set it before Execute),
		// so a per-tenant / version-pinned step module resolves correctly.
		t, err := r.Resolve(ctx, moduleID)
		return t, err == nil
	}
	return engine.Default.Get(moduleID)
}

func executeForEach(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	stepModule, err := params.String(job.Params, "step_module")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	stepParams, _ := job.Params["step_params"].(map[string]any)
	itemPort, _ := job.Params["item_port"].(string)
	if itemPort == "" {
		itemPort = "in"
	}
	failFast, _ := job.Params["fail_fast"].(bool)
	concurrency, _ := paramInt(job.Params, "concurrency")
	if concurrency < 0 {
		concurrency = 0
	}

	itemsRef, ok := job.Input["items"]
	if !ok {
		return params.Err(job, "missing_input", "items input is required"), nil
	}
	items, err := normalizeItems(itemsRef)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	transport, ok := resolveStep(ctx, stepModule)
	if !ok {
		return params.Err(job, "unknown_step", fmt.Sprintf("step module %q is not registered", stepModule)), nil
	}

	if len(items) == 0 {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"results": {MIME: MIMEList, Inline: []core.Ref{}},
				"errors":  {MIME: "application/json", Inline: map[string]any{}},
			},
		}, nil
	}

	results := make([]core.Ref, len(items))
	errors := make(map[string]any)
	var errorsMu sync.Mutex

	gate := concurrency
	if gate == 0 || gate > len(items) {
		gate = len(items)
	}
	sem := make(chan struct{}, gate)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i, item := range items {
		if runCtx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, value core.Ref) {
			defer wg.Done()
			defer func() { <-sem }()

			if runCtx.Err() != nil {
				return
			}

			resolvedParams, err := substituteItemParams(runCtx, stepParams, value)
			if err != nil {
				errorsMu.Lock()
				errors[fmt.Sprintf("%d", idx)] = map[string]any{
					"code":    "template",
					"message": err.Error(),
				}
				errorsMu.Unlock()
				if failFast {
					cancel()
				}
				return
			}
			subJob := core.Job{
				ID:            fmt.Sprintf("%s#%d", job.ID, idx),
				GraphID:       job.GraphID,
				NodeID:        fmt.Sprintf("%s[%d]", job.NodeID, idx),
				TraceID:       job.TraceID,
				SpanID:        job.SpanID,
				Input:         map[string]core.Ref{itemPort: value},
				Params:        resolvedParams,
				Env:           job.Env,
				Cleanup:       core.CleanupOnNodeComplete,
				WorkspaceRoot: job.WorkspaceRoot,
				Tenant:        job.Tenant,
				QuotaLimit:    job.QuotaLimit,
				QuotaUsed:     job.QuotaUsed,
			}

			stepResult, execErr := transport.Execute(runCtx, subJob, nil)
			emitProgress(progress, job, float64(idx+1)/float64(len(items)),
				fmt.Sprintf("item %d/%d done", idx+1, len(items)))

			results[idx] = core.Ref{
				MIME:   "application/json",
				Inline: stepResultPayload(stepResult),
			}

			if execErr != nil || stepResult.Status == core.StatusError {
				errorsMu.Lock()
				if execErr != nil {
					errors[fmt.Sprintf("%d", idx)] = execErr.Error()
				} else {
					errors[fmt.Sprintf("%d", idx)] = errorPayload(stepResult.Error)
				}
				errorsMu.Unlock()
				if failFast {
					cancel()
				}
			}
		}(i, item)
	}
	wg.Wait()

	status := core.StatusOK
	var jobErr *core.JobError
	if len(errors) > 0 && failFast {
		status = core.StatusError
		jobErr = &core.JobError{
			Code:    "item_failed",
			Message: fmt.Sprintf("for_each aborted: %d/%d items failed", len(errors), len(items)),
		}
	}

	return core.Result{
		JobID:  job.ID,
		Status: status,
		Output: map[string]core.Ref{
			"results": {MIME: MIMEList, Inline: results},
			"errors":  {MIME: "application/json", Inline: errors},
		},
		Error: jobErr,
	}, nil
}

// capItems rejects an items list bigger than the row ceiling. for_each
// allocates a result slot (and runs a sub-job) per item, so an unbounded list
// is an OOM vector; fail fast before materializing it.
func capItems(n int) error {
	if max := limits.MaxRows(); n > max {
		return fmt.Errorf("items list has %d entries, exceeds the %d-item limit (raise HAZYFLOW_MAX_ROWS to iterate larger lists)", n, max)
	}
	return nil
}

// normalizeItems coerces the items input into a list of Refs that can be
// fed into the step. The list arrives as either:
//   - []core.Ref     (from merge or another for_each)
//   - []any          (parsed JSON array, the common webhook case)
//   - []map[string]any
func normalizeItems(ref core.Ref) ([]core.Ref, error) {
	switch v := ref.Inline.(type) {
	case []core.Ref:
		if err := capItems(len(v)); err != nil {
			return nil, err
		}
		return v, nil
	case []any:
		if err := capItems(len(v)); err != nil {
			return nil, err
		}
		out := make([]core.Ref, len(v))
		for i, item := range v {
			out[i] = core.Ref{Inline: item}
		}
		return out, nil
	case []map[string]any:
		if err := capItems(len(v)); err != nil {
			return nil, err
		}
		out := make([]core.Ref, len(v))
		for i, item := range v {
			out[i] = core.Ref{Inline: item}
		}
		return out, nil
	case nil:
		return nil, fmt.Errorf("items input has no inline value")
	default:
		return nil, fmt.Errorf("items input must be a list, got %T", v)
	}
}

func stepResultPayload(r core.Result) map[string]any {
	payload := map[string]any{"status": r.Status}
	if len(r.Output) > 0 {
		payload["output"] = r.Output
	}
	if r.Error != nil {
		payload["error"] = errorPayload(r.Error)
	}
	return payload
}

func errorPayload(e *core.JobError) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{"code": e.Code, "message": e.Message}
}

// substituteItemParams produces a per-iteration copy of params with every
// ${item.path} placeholder replaced by the corresponding field of the
// current item. The original params map is not mutated so concurrent
// iterations don't race on each other's substitutions.
//
// Path syntax: dot-separated keys/indices. `${item.user.name}` walks
// item.user.name; `${item.tags.0}` reads tags[0]; `${item.}` (empty path)
// stringifies the whole item. Missing fields fail the iteration.
func substituteItemParams(ctx context.Context, params map[string]any, item core.Ref) (map[string]any, error) {
	if len(params) == 0 {
		return params, nil
	}
	copied := deepCopyMap(params)
	sub := itemSubstituter(item.Inline)
	resolved, err := engine.SubstituteValue(ctx, copied, sub)
	if err != nil {
		return nil, err
	}
	return resolved.(map[string]any), nil
}

// itemSubstituter returns a Substituter that resolves ${item.path}. Any
// other scheme is left alone — secret schemes were already resolved by
// the engine on the parent for_each job's params.
func itemSubstituter(item any) engine.Substituter {
	return func(_ context.Context, scheme, path string) (string, bool, error) {
		if scheme != "item" {
			return "", false, nil
		}
		value, err := traverseItemPath(item, path)
		if err != nil {
			return "", true, err
		}
		return stringifyItemValue(value), true, nil
	}
}

func traverseItemPath(root any, path string) (any, error) {
	if path == "" {
		return root, nil
	}
	current := root
	parts := strings.Split(path, ".")
	for i, part := range parts {
		switch typed := current.(type) {
		case map[string]any:
			v, ok := typed[part]
			if !ok {
				return nil, fmt.Errorf("missing key %q at %s", part, strings.Join(parts[:i+1], "."))
			}
			current = v
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("index %q is not a number at %s", part, strings.Join(parts[:i+1], "."))
			}
			if idx < 0 || idx >= len(typed) {
				return nil, fmt.Errorf("index %d out of range (len=%d) at %s", idx, len(typed), strings.Join(parts[:i+1], "."))
			}
			current = typed[idx]
		default:
			return nil, fmt.Errorf("cannot traverse %T at %s", current, strings.Join(parts[:i+1], "."))
		}
	}
	return current, nil
}

// stringifyItemValue produces the string representation used to splice a
// resolved item value into params. Strings pass through unquoted; numbers
// and booleans use their natural form; complex values fall back to JSON
// so they can be embedded into URL paths, headers, or templated bodies.
func stringifyItemValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

// deepCopyMap clones a map[string]any-shaped tree so iteration-time
// substitution mutates a private copy. JSON-ish trees are common (the
// shape we get from webhook bodies) so a JSON round-trip would also work,
// but it'd discard non-JSON values like pointers passed in tests.
func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopyValue(e)
		}
		return out
	default:
		return v
	}
}
