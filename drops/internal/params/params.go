// SPDX-FileCopyrightText: 2026 Angels' Ware
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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

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

func StringDefault(params map[string]any, key, def string) string {
	if s, ok := StringOpt(params, key); ok {
		return s
	}
	return def
}

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

func Bool(params map[string]any, key string) (bool, bool) {
	v, ok := params[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

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

func ClampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func Err(job core.Job, code, msg string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg},
	}
}

func ErrDetails(job core.Job, code, msg, details string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: msg, Details: details},
	}
}

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

func TimeoutMS(job core.Job, def int) int {
	ms := IntDefault(job.Params, "timeout_ms", def)
	if ms <= 0 {
		ms = def
	}
	return ms
}

func Truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return s[:limit]
	}
	return s
}

func JSONFieldMessage(body []byte, field string, limit int) string {
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		if s, ok := m[field].(string); ok && s != "" {
			return s
		}
	}
	return Truncate(string(body), limit)
}

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

func RequestBody(job core.Job) (io.Reader, error) {
	if input, ok := job.Input["request_body"]; ok {
		switch v := input.Inline.(type) {
		case string:
			return strings.NewReader(v), nil
		case []byte:
			return bytes.NewReader(v), nil
		case nil:
		default:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("marshal request_body: %w", err)
			}
			return bytes.NewReader(b), nil
		}
	}
	if s, ok := job.Params["body"].(string); ok && s != "" {
		return strings.NewReader(s), nil
	}
	return nil, nil
}

func StatusAccepted(got int, expect []int) bool {
	if len(expect) == 0 {
		return got >= 200 && got < 300
	}
	for _, e := range expect {
		if got == e {
			return true
		}
	}
	return false
}
