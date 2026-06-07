// Package sheets hosts the native Google Sheets connectors
// (sheets_read_range, sheets_append_row, sheets_export_pdf), migrated from
// the scripted TS drops. They authenticate with Google OAuth (the "google"
// provider) via the SetTokenLookup hook the daemon wires at startup, or an
// explicit `token` param.
package sheets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body. Generous enough for the
// PDF export path.
const maxResponseBytes = 64 << 20 // 64 MiB

const (
	sheetsAPIBase = "https://sheets.googleapis.com/v4"
	driveAPIBase  = "https://www.googleapis.com/drive/v3"
)

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
		return "", fmt.Errorf("no Google token: pass `token` directly or connect a Google account via /api/v1/oauth/google/authorize")
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

// Test seams: the read/append drops hit the Sheets API; export hits Drive.
var (
	baseMu     sync.RWMutex
	sheetsBase = sheetsAPIBase
	driveBase  = driveAPIBase
)

// SetHTTPBases swaps both API roots (tests point them at one httptest server).
func SetHTTPBases(sheets, drive string) {
	baseMu.Lock()
	defer baseMu.Unlock()
	sheetsBase, driveBase = sheets, drive
}

func sheetsBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	baseMu.RLock()
	defer baseMu.RUnlock()
	return sheetsBase
}

func driveBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	baseMu.RLock()
	defer baseMu.RUnlock()
	return driveBase
}

func googleDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// base_url is a tenant-supplied param, so guard the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress
	// allowlist (when set) bounds which public hosts the bearer token
	// may be sent to.
	if err := hfnet.EgressAllowed(url); err != nil {
		return 0, nil, err
	}
	resp, err := hfnet.SafeHTTPClient(time.Duration(timeoutMS)*time.Millisecond, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(raw)) > maxResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("google response exceeds %d bytes", maxResponseBytes)
	}
	return resp.StatusCode, raw, nil
}

func sheetsErr(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(body) > 512 {
		return string(body[:512])
	}
	return string(body)
}

var sheetIDRe = regexp.MustCompile(`/d/([a-zA-Z0-9-_]+)`)

// sheetID extracts the spreadsheet ID from a full Google Sheets URL, or
// returns the input unchanged when it's already an ID.
func sheetID(raw string) string {
	if m := sheetIDRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return raw
}

// flattenValues turns a Sheets values matrix into rows+headers. With
// useHeaders, the first row names the columns; otherwise columns are
// col_0, col_1, … and every row is data.
func flattenValues(raw [][]any, useHeaders bool) ([]string, []map[string]any) {
	if len(raw) == 0 {
		return []string{}, []map[string]any{}
	}
	var headers []string
	var data [][]any
	if useHeaders {
		for _, v := range raw[0] {
			headers = append(headers, cell(v))
		}
		data = raw[1:]
	} else {
		maxCols := 0
		for _, r := range raw {
			if len(r) > maxCols {
				maxCols = len(r)
			}
		}
		for i := 0; i < maxCols; i++ {
			headers = append(headers, fmt.Sprintf("col_%d", i))
		}
		data = raw
	}
	rows := make([]map[string]any, 0, len(data))
	for _, r := range data {
		rec := make(map[string]any, len(headers))
		for i, h := range headers {
			if i < len(r) {
				rec[h] = r[i]
			} else {
				rec[h] = ""
			}
		}
		rows = append(rows, rec)
	}
	return headers, rows
}

func cell(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func deriveHeaders(rows []map[string]any) []string {
	seen := map[string]struct{}{}
	for _, r := range rows {
		for k := range r {
			seen[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalizeRows coerces the rows input into a slice of objects. Mirrors
// the transform/db contract: a list of objects, a single object, or JSON.
func normalizeRows(inline any) ([]map[string]any, error) {
	switch v := inline.(type) {
	case nil:
		return nil, nil
	case []map[string]any:
		return v, nil
	case []any:
		out := make([]map[string]any, 0, len(v))
		for i, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("row %d: expected object, got %T", i, item)
			}
			out = append(out, m)
		}
		return out, nil
	case map[string]any:
		return []map[string]any{v}, nil
	case string:
		var parsed []map[string]any
		if v == "" {
			return nil, nil
		}
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, fmt.Errorf("rows JSON: %w", err)
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("rows: unsupported input type %T", inline)
}

func normalizeHeaders(inline any) []string {
	switch v := inline.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, h := range v {
			out = append(out, cell(h))
		}
		return out
	}
	return nil
}
