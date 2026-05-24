package flow

import (
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
)

func paramInt(params map[string]any, key string) (int, error) {
	v, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("missing param %q", key)
	}
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case float64:
		return int(x), nil
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return 0, fmt.Errorf("param %q: %w", key, err)
		}
		return int(i), nil
	default:
		return 0, fmt.Errorf("param %q: expected number, got %T", key, v)
	}
}

func paramString(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing param %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("param %q: expected string, got %T", key, v)
	}
	return s, nil
}

func errResult(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

func emitProgress(ch chan<- core.Progress, job core.Job, pct float64, msg string) {
	if ch == nil {
		return
	}
	select {
	case ch <- core.Progress{JobID: job.ID, NodeID: job.NodeID, Percent: &pct, Message: msg}:
	default:
	}
}
