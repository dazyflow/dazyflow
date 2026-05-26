package ai

import (
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
)

func paramFloat(params map[string]any, key string) (float64, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func paramStringSlice(params map[string]any, key string) ([]string, bool) {
	v, ok := params[key]
	if !ok {
		return nil, false
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// paramMessages parses the messages param into the claude wire shape.
// Accepts an array of {role, content} objects where content is a string
// (the simple case). Tool-use multi-block content is out of scope for v1.
func paramMessages(params map[string]any, key string) ([]claudeMessage, error) {
	v, ok := params[key]
	if !ok {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("param %q: expected array, got %T", key, v)
	}
	out := make([]claudeMessage, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d]: expected object, got %T", key, i, item)
		}
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		if role == "" || content == "" {
			return nil, fmt.Errorf("%s[%d]: requires role and content", key, i)
		}
		out = append(out, claudeMessage{Role: role, Content: content})
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
