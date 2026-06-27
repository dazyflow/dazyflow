// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package params hosts the tiny param-extraction + error-result
// helpers every integration drop reaches for. Lives under
// drops/internal/ so only sibling integration packages can
// import it — keeps the helpers internal to the connector layer
// without exposing them as a public API surface.
//
// Why centralized rather than per-package (as it was originally):
// the bodies are 5–10 lines each and never diverged across the 14
// integration packages. Maintaining 14 copies cost more than the
// import dependency the original design was avoiding.
package params

import (
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
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

// Bool returns the bool at key and whether a usable bool was present.
// The second result lets callers tell "explicitly set to false" apart
// from "absent" — e.g. an override that should only apply when the
// param was actually provided. Use BoolDefault when that distinction
// doesn't matter.
func Bool(params map[string]any, key string) (bool, bool) {
	v, ok := params[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
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

// IntSlice returns the param at key as []int, accepting a []any of
// int/int64/float64 items (JSON arrays decode to []any of float64) or a
// native []int / []int64. Missing or wrong-typed items are skipped; a
// missing or non-array param returns nil. Supersedes the per-package
// paramIntSlice / paramIntSliceLocal copies.
func IntSlice(params map[string]any, key string) []int {
	v, ok := params[key]
	if !ok {
		return nil
	}
	switch arr := v.(type) {
	case []int:
		return arr
	case []int64:
		out := make([]int, len(arr))
		for i, n := range arr {
			out[i] = int(n)
		}
		return out
	case []any:
		out := make([]int, 0, len(arr))
		for _, item := range arr {
			switch n := item.(type) {
			case int:
				out = append(out, n)
			case int64:
				out = append(out, int(n))
			case float64:
				out = append(out, int(n))
			}
		}
		return out
	}
	return nil
}

// StringSlice returns the param at key as []string, accepting a native
// []string or a []any whose string items are kept (non-strings skipped). A
// missing or non-array param returns nil. Supersedes the per-package
// paramStringSlice copies.
func StringSlice(params map[string]any, key string) []string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	switch arr := v.(type) {
	case []string:
		return arr
	case []any:
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ClampInt constrains v to the inclusive [lo, hi] range.
func ClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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

// TextInputOr returns the text wired into input port `port` (string or raw
// bytes), or `fallback` when the port is unwired/empty. ok is false only when
// the port carries a NON-text value — a wiring mistake the caller rejects.
// This was a byte-identical ~18-line copy in every action connector (stripe,
// twilio, discord, homeassistant, gmail send, notion, mqtt, github, slack);
// it lives here once. Same "input overrides param" pattern across all of them.
func TextInputOr(job core.Job, port, fallback string) (val string, ok bool) {
	in, present := job.Input[port]
	if !present || in.Inline == nil {
		return fallback, true
	}
	switch v := in.Inline.(type) {
	case string:
		if v != "" {
			return v, true
		}
		return fallback, true
	case []byte:
		if len(v) > 0 {
			return string(v), true
		}
		return fallback, true
	}
	return "", false
}

// EmitProgress sends a progress update on ch, no-op when ch is nil and
// non-blocking when the channel is full (a slow consumer never stalls the
// drop). The byte-identical emitProgress every drop carried.
func EmitProgress(ch chan<- core.Progress, job core.Job, pct float64, msg string) {
	if ch == nil {
		return
	}
	select {
	case ch <- core.Progress{JobID: job.ID, NodeID: job.NodeID, Percent: &pct, Message: msg}:
	default:
	}
}

// TimeoutMS returns the "timeout_ms" param clamped to a positive value,
// falling back to def when it is missing or non-positive — the clamp every
// HTTP drop applied before handing the value to net.Do.
func TimeoutMS(job core.Job, def int) int {
	ms := IntDefault(job.Params, "timeout_ms", def)
	if ms <= 0 {
		ms = def
	}
	return ms
}

// Truncate trims surrounding whitespace from s and caps it at limit bytes —
// the raw-body fallback the error extractors share.
func Truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return s[:limit]
	}
	return s
}

// JSONFieldMessage pulls a human message out of an error body that carries it
// under a single named string field ({"message":…}, {"reason":…}), falling
// back to Truncate(rawBody, limit) when the field is absent or empty.
func JSONFieldMessage(body []byte, field string, limit int) string {
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		if s, ok := m[field].(string); ok && s != "" {
			return s
		}
	}
	return Truncate(string(body), limit)
}

// APIErrorMessage pulls a human message out of a flat {message, code} error
// body — the shape Twilio and Discord both return — formatting it as
// "code: message" when a non-zero code is present, else the bare message.
// Falls back to the raw body truncated at limit bytes. Vendors whose error
// JSON nests differently (Stripe's {error:{…}}, Google's, GitHub's) keep
// their own extractor.
func APIErrorMessage(body []byte, limit int) string {
	var e struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Message != "" {
		if e.Code != 0 {
			return fmt.Sprintf("%d: %s", e.Code, e.Message)
		}
		return e.Message
	}
	if len(body) > limit {
		return string(body[:limit])
	}
	return string(body)
}

// HTTPFailure maps the transport-error / non-2xx epilogue every HTTP drop
// shares to an error Result, returning nil when the call succeeded. err
// non-nil is a transport failure → "<vendor>_http_error". A non-2xx status →
// "<vendor>_error" with "<Vendor label> returned <status>: <extract(body)>";
// the vendor-facing label and the body extractor stay per-connector so the
// exact messages tests assert on are preserved. Pass the human label the
// connector used (e.g. "Stripe", "Twilio") as vendorLabel.
func HTTPFailure(job core.Job, vendor, vendorLabel string, status int, body []byte, err error, extract func([]byte) string) *core.Result {
	if err != nil {
		r := Err(job, vendor+"_http_error", err.Error())
		return &r
	}
	if status < 200 || status >= 300 {
		r := Err(job, vendor+"_error", fmt.Sprintf("%s returned %d: %s", vendorLabel, status, extract(body)))
		return &r
	}
	return nil
}
