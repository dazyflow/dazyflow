// Package sheets hosts the Google Sheets launch connector — third
// T1 stop, the natural pair for Gmail (same Google OAuth app, no
// new auth wiring needed). Two action drops cover the common
// Zapier-shape patterns:
//
//	sheets_append_row  — append rows to a sheet (the "log this" sink)
//	sheets_read_range  — read a range as rows (the "consume tabular data" source)
//
// Both speak the same `rows` ([]{column:value}) + `headers` ([]string)
// shape every other tabular drop in this codebase uses, so a graph
// can do `excel_read → map_rows → sheets_append_row` or
// `sheets_read_range → postgres_insert_rows` without any reshaping
// between nodes.
package sheets

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

// TokenLookup matches the per-connector shape used by slack/gmail.
// Sheets shares the "google" OAuth app with Gmail; the same access
// token works for both APIs as long as the Sheets scope was in the
// authorize request.
type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	if t, _ := params.StringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := params.StringOpt(job.Params, "account")
	if account == "" {
		account = "default"
	}
	tokenLookupMu.RLock()
	fn := tokenLookup
	tokenLookupMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("no Sheets token: pass `token` directly or connect a Google account via /api/v1/oauth/google/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("google account %q is not connected", account)
	}
	return tok, nil
}

var (
	httpBaseMu sync.RWMutex
	httpBase   = "https://sheets.googleapis.com/v4"
	// driveBase hosts Drive API v3, used by sheets_export_pdf via
	// files.export (Sheets has no native export-to-PDF — Drive does
	// the render and Sheets shares the Google OAuth grant, so the
	// same access token works once drive.readonly is in the scope
	// list).
	driveBase = "https://www.googleapis.com/drive/v3"
)

func SetHTTPBase(base string) {
	httpBaseMu.Lock()
	defer httpBaseMu.Unlock()
	httpBase = base
}

// SetDriveHTTPBase swaps the Drive API root; tests point this at an
// httptest server so they never hit Google. Independent setter so a
// test can keep sheets.googleapis.com on its real base while only
// stubbing Drive (or vice versa).
func SetDriveHTTPBase(base string) {
	httpBaseMu.Lock()
	defer httpBaseMu.Unlock()
	driveBase = base
}

func currentHTTPBase() string {
	httpBaseMu.RLock()
	defer httpBaseMu.RUnlock()
	return httpBase
}

func currentDriveHTTPBase() string {
	httpBaseMu.RLock()
	defer httpBaseMu.RUnlock()
	return driveBase
}

// ---- shared row/header normalization --------------------------------
//
// Same shape as integrations/db and integrations/transform — accept
// the native []map[string]any AND the post-JSON-roundtrip []any of
// map[string]any. Duplicated rather than imported so this package
// doesn't drag in db/transform.

func normalizeRows(inline any) ([]map[string]any, error) {
	if inline == nil {
		return nil, nil
	}
	switch v := inline.(type) {
	case []map[string]any:
		return v, nil
	case []map[string]string:
		out := make([]map[string]any, len(v))
		for i, r := range v {
			m := make(map[string]any, len(r))
			for k, val := range r {
				m[k] = val
			}
			out[i] = m
		}
		return out, nil
	case []any:
		out := make([]map[string]any, 0, len(v))
		for i, item := range v {
			m, err := coerceRowMap(item)
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", i, err)
			}
			out = append(out, m)
		}
		return out, nil
	case string:
		var parsed []map[string]any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, fmt.Errorf("rows JSON: %w", err)
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("rows: unsupported input type %T", inline)
}

func coerceRowMap(item any) (map[string]any, error) {
	switch m := item.(type) {
	case map[string]any:
		return m, nil
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected object, got %T", item)
}

func normalizeHeaders(inline any) ([]string, error) {
	switch v := inline.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, len(v))
		for i, h := range v {
			s, ok := h.(string)
			if !ok {
				return nil, fmt.Errorf("headers[%d]: expected string, got %T", i, h)
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("headers: unsupported input type %T", inline)
}

func deriveHeaders(rows []map[string]any) []string {
	seen := map[string]struct{}{}
	for _, r := range rows {
		for k := range r {
			seen[k] = struct{}{}
		}
	}
	headers := make([]string, 0, len(seen))
	for k := range seen {
		headers = append(headers, k)
	}
	sort.Strings(headers)
	return headers
}

// ---- Sheets error envelope -----------------------------------------
//
// Google APIs share an error shape (`{"error":{"code","message","status"}}`)
// across services. Sheets returns it verbatim; same extractor as
// integrations/gmail kept local to avoid a cross-connector import.

type sheetsErrorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func extractSheetsError(body []byte) string {
	var env sheetsErrorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	if len(body) > 512 {
		return string(body[:512]) + "…"
	}
	return string(body)
}
