package flow

import (
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/hazyflow/core"
)

func paramInt(params map[string]any, key string) (int, error) {
	v, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("missing param %q", key)
	}
	if n, ok := coerceInt(v); ok {
		return n, nil
	}
	return 0, fmt.Errorf("param %q: expected number, got %T", key, v)
}

// coerceInt converts a JSON-ish numeric value to int, accepting the same
// types paramInt does. Used to read numbers that arrive via a wired input
// ref (core.Ref.Inline) rather than from params. Returns false for nil or
// any non-numeric value.
func coerceInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
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
