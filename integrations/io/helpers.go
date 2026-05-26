package io

import (
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

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
	if s, ok := params.StringOpt(job.Params, port); ok {
		return s
	}
	return ""
}
