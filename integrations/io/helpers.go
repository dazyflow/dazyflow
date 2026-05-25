package io

import (
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
)

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

func paramStringOpt(params map[string]any, key string) (string, bool) {
	v, ok := params[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func paramBool(params map[string]any, key string) (bool, bool) {
	v, ok := params[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// paramInt accepts JSON numbers (float64), Go ints, or int64.
// Returns (value, true) when the key is present and numeric.
func paramInt(params map[string]any, key string) (int, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

func errResult(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

// pickPath resolves a path-like input with consistent precedence:
// wired input port wins (`file_picker → excel_read` etc.), then
// params.<port>. Returns "" when neither is set so callers can
// surface their own error message.
//
// Accepts both a plain-string Inline and a file Ref shape: when the
// upstream is a file_picker, the input port carries Inline=path
// (string); when an old graph wires a file Ref directly, Ref.Ref
// carries the same path. Both work.
func pickPath(job core.Job, port string) string {
	if ref, ok := job.Input[port]; ok {
		if s, ok := ref.Inline.(string); ok && s != "" {
			return s
		}
		if ref.Ref != "" {
			return ref.Ref
		}
	}
	if s, ok := paramStringOpt(job.Params, port); ok {
		return s
	}
	return ""
}
