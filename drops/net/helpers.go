package net

import (
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
)

func paramBool(params map[string]any, key string) (bool, bool) {
	v, ok := params[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func paramIntSlice(params map[string]any, key string) []int {
	v, ok := params[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(arr))
	for _, item := range arr {
		switch x := item.(type) {
		case int:
			out = append(out, x)
		case int64:
			out = append(out, int(x))
		case float64:
			out = append(out, int(x))
		}
	}
	return out
}

func paramHeaders(params map[string]any, key string) (map[string]string, error) {
	v, ok := params[key]
	if !ok {
		return nil, nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("param %q: expected object, got %T", key, v)
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("header %q: expected string, got %T", k, val)
		}
		out[k] = s
	}
	return out, nil
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
