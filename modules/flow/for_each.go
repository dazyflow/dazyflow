package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeNode{
		Manifest: core.Manifest{
			ID:             "for_each",
			Version:        "1.0",
			Label:          "For each",
			Color:          "#a679d4",
			Category:       "flow_control",
			Provider:       "internal",
			Tags:           []string{"iterate", "loop", "fan_out", "map"},
			Description:    "Run a configured step module once per item in an input list. Items execute in parallel up to params.concurrency. Outputs `results` (one Result per item, in order) and `errors` (a map of failing indices). Set fail_fast=true to abort on the first failure; otherwise the iteration continues and per-item errors surface on the errors port.",
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

func executeForEach(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	stepModule, err := paramString(job.Params, "step_module")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
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
		return errResult(job, "missing_input", "items input is required"), nil
	}
	items, err := normalizeItems(itemsRef)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	transport, ok := engine.Default.Get(stepModule)
	if !ok {
		return errResult(job, "unknown_step", fmt.Sprintf("step module %q is not registered", stepModule)), nil
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

			subJob := core.Job{
				ID:            fmt.Sprintf("%s#%d", job.ID, idx),
				GraphID:       job.GraphID,
				NodeID:        fmt.Sprintf("%s[%d]", job.NodeID, idx),
				TraceID:       job.TraceID,
				SpanID:        job.SpanID,
				Input:         map[string]core.Ref{itemPort: value},
				Params:        stepParams,
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

// normalizeItems coerces the items input into a list of Refs that can be
// fed into the step. The list arrives as either:
//   - []core.Ref     (from merge or another for_each)
//   - []any          (parsed JSON array, the common webhook case)
//   - []map[string]any
func normalizeItems(ref core.Ref) ([]core.Ref, error) {
	switch v := ref.Inline.(type) {
	case []core.Ref:
		return v, nil
	case []any:
		out := make([]core.Ref, len(v))
		for i, item := range v {
			out[i] = core.Ref{Inline: item}
		}
		return out, nil
	case []map[string]any:
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
