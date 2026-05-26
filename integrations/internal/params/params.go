// Package params hosts the tiny param-extraction + error-result
// helpers every integration drop reaches for. Lives under
// integrations/internal/ so only sibling integration packages can
// import it — keeps the helpers internal to the connector layer
// without exposing them as a public API surface.
//
// Why centralized rather than per-package (as it was originally):
// the bodies are 5–10 lines each and never diverged across the 14
// integration packages. Maintaining 14 copies cost more than the
// import dependency the original design was avoiding.
package params

import (
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// String returns a required string param. Error messages follow the
// "missing param %q" / "param %q: expected string, got %T" pattern
// integrations have used everywhere — preserving the exact text so
// existing tests that match on these messages keep passing after the
// migration.
func String(params map[string]any, key string) (string, error) {
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

// StringOpt returns (value, true) when the param is present and a
// string. Absence and wrong-type both return ("", false) — callers
// distinguish via the bool, not by inspecting an error.
func StringOpt(params map[string]any, key string) (string, bool) {
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

// StringDefault returns the string at key, falling back to def when
// the param is missing or not a string.
func StringDefault(params map[string]any, key, def string) string {
	if s, ok := StringOpt(params, key); ok {
		return s
	}
	return def
}

// IntDefault returns the int at key, accepting int / int64 / float64
// (JSON numbers come through as float64). Anything else — including
// strings that look numeric — returns def, mirroring the per-package
// behavior the helpers replaced.
func IntDefault(params map[string]any, key string, def int) int {
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return def
}

// BoolDefault returns the bool at key, falling back to def for
// missing / wrong-type values.
func BoolDefault(params map[string]any, key string, def bool) bool {
	v, ok := params[key]
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

// Err builds a status=error Result with the given code + message.
// The shape every integration uses to bail out of Execute when a
// param is wrong, an HTTP call failed, or an upstream port had bad
// data.
func Err(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

// ErrDetails extends Err with a technical Details string. Use when
// the user-facing Message is too vague to debug from alone — the
// Details carries the type signature, library error string, or
// other developer hint the UI tucks behind a "Details" expander.
func ErrDetails(job core.Job, code, msg, details string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg, Details: details},
	}
}
