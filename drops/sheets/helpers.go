// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sheets hosts the native Google Sheets connectors
// (sheets_read_range, sheets_append_row, sheets_export_pdf), migrated from
// the scripted TS drops. They authenticate with Google OAuth (the "google"
// provider) via the SetTokenLookup hook the daemon wires at startup.
package sheets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/apibase"
	"git.sr.ht/~klahr/dazyflow/drops/internal/google"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream can't OOM the daemon by streaming an unbounded
// body. Generous enough for the PDF export path.
const maxResponseBytes = 64 << 20 // 64 MiB

const (
	sheetsAPIBase = "https://sheets.googleapis.com/v4"
	driveAPIBase  = "https://www.googleapis.com/drive/v3"
)

// SetTokenLookup wires the shared Google OAuth token resolver (one provider
// serves every Google connector — see drops/internal/google). Retained as a
// package entry point for tests.
func SetTokenLookup(fn google.TokenLookup) { google.SetTokenLookup(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return google.ResolveToken(ctx, job)
}

// Test seams: the read/append drops hit the Sheets API; export hits Drive.
var (
	sheetsBase = apibase.New(sheetsAPIBase)
	driveBase  = apibase.New(driveAPIBase)
)

// SetHTTPBases swaps both API roots (tests point them at one httptest server).
func SetHTTPBases(sheets, drive string) {
	sheetsBase.Set(sheets)
	driveBase.Set(drive)
}

// base_url is no longer a user-facing param (removed from the schema), but
// like `token` the engine still honors it when present — the integration
// tests point it at an httptest server. The SafeHTTPClient + egress guard in
// googleDo still bound where the bearer token can be sent. The override is
// used verbatim (no trailing-slash trim — endpoints are concatenated as-is).
func sheetsBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	return sheetsBase.Get()
}

func driveBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	return driveBase.Get()
}

func googleDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	return google.Do(ctx, method, url, token, contentType, body, timeoutMS, maxResponseBytes)
}

func sheetsErr(body []byte) string { return google.ErrMessage(body, 512) }

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

// columnMapping is one row of the sheets_append_row `mapping` param:
// the named sheet column and the field (key/path) in each incoming row
// whose value fills it.
type columnMapping struct {
	Column string
	Source string
}

// parseMapping reads the optional `mapping` param — an array of
// {column, source} objects (JSON-decoded as []any of map[string]any).
// Entries without a column are skipped; a missing/non-array param yields
// nil (the header-derivation path). A JSON string is also accepted so the
// value can round-trip through a text field.
func parseMapping(p map[string]any) []columnMapping {
	raw, ok := p["mapping"]
	if !ok || raw == nil {
		return nil
	}
	if s, isStr := raw.(string); isStr {
		if s == "" {
			return nil
		}
		var decoded []map[string]any
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			return nil
		}
		arr := make([]any, len(decoded))
		for i, m := range decoded {
			arr[i] = m
		}
		raw = arr
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]columnMapping, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		col, _ := m["column"].(string)
		if col == "" {
			continue
		}
		src, _ := m["source"].(string)
		out = append(out, columnMapping{Column: col, Source: src})
	}
	return out
}

// resolveSpreadsheetID picks the spreadsheet to act on: a wired
// 'spreadsheet_id' input port wins over the picked param, so a spreadsheet
// reference can be threaded in from an upstream sheet step (e.g. append row's
// 'spreadsheet_id' output). Either form may be a full URL or a bare id —
// sheetID extracts the id. Empty input falls back to the param.
func resolveSpreadsheetID(job core.Job) string {
	if in, ok := job.Input["spreadsheet_id"]; ok && in.Inline != nil {
		switch v := in.Inline.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return sheetID(s)
			}
		case []byte:
			if s := strings.TrimSpace(string(v)); s != "" {
				return sheetID(s)
			}
		}
	}
	return sheetID(params.StringDefault(job.Params, "spreadsheet_id", ""))
}

// quoteSheetTab wraps a tab name in single quotes for A1 notation so names
// with spaces or punctuation parse (e.g. 'Inbox Log'!A1). Embedded single
// quotes are doubled, per the Sheets reference grammar.
func quoteSheetTab(tab string) string {
	return "'" + strings.ReplaceAll(tab, "'", "''") + "'"
}

// readSheetHeaders fetches the first row of a tab as its existing column
// headers, left-to-right. An empty sheet (no first row) yields nil. The
// append drop uses this to place each mapped value under its named column
// by position — so the column you map to decides where the value lands,
// independent of the mapping rows' order.
func readSheetHeaders(ctx context.Context, job core.Job, id, tab, token string, timeoutMS int) ([]string, error) {
	rng := quoteSheetTab(tab) + "!1:1"
	q := url.Values{}
	q.Set("majorDimension", "ROWS")
	endpoint := sheetsBaseURL(job) + "/spreadsheets/" + url.PathEscape(id) + "/values/" + url.PathEscape(rng) + "?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, timeoutMS)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", sheetsErr(body))
	}
	var parsed struct {
		Values [][]any `json:"values"`
	}
	_ = json.Unmarshal(body, &parsed)
	if len(parsed.Values) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(parsed.Values[0]))
	for _, c := range parsed.Values[0] {
		if c == nil {
			out = append(out, "")
			continue
		}
		out = append(out, fmt.Sprintf("%v", c))
	}
	return out, nil
}

func mappingColumns(cmap []columnMapping) []string {
	cols := make([]string, len(cmap))
	for i, c := range cmap {
		cols[i] = c.Column
	}
	return cols
}

// projectRows rebuilds each incoming row as an object keyed by the mapping's
// columns, pulling each column's value from the row at the mapped source
// field. A missing source yields "" (matching the append builder's blank
// fill). The downstream values-matrix builder then reads row[column] for
// each header in order.
func projectRows(rows []map[string]any, cmap []columnMapping) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		rec := make(map[string]any, len(cmap))
		for _, c := range cmap {
			rec[c.Column] = lookupField(row, c.Source)
		}
		out = append(out, rec)
	}
	return out
}

// lookupField reads source from row, supporting dotted paths (e.g.
// "user.email") for nested objects. Returns "" when absent so a missing
// field becomes a blank cell rather than a JSON null.
func lookupField(row map[string]any, source string) any {
	if source == "" {
		return ""
	}
	var cur any = row
	for _, part := range strings.Split(source, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		v, ok := m[part]
		if !ok {
			return ""
		}
		cur = v
	}
	return cur
}

// ListSheetColumns reads the header row (row 1) of a spreadsheet tab and
// returns each non-empty header as a {id, name} option (id == name == the
// header text). It's the backend for the mapping editor's "Sheet column"
// dropdown, so a user maps onto real, existing columns instead of typing.
// Depends on the chosen spreadsheet_id and range (tab); reads account from
// job.Params. An empty header row yields no options.
func ListSheetColumns(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	id := sheetID(params.StringDefault(job.Params, "spreadsheet_id", ""))
	if id == "" {
		return nil, fmt.Errorf("spreadsheet_id is required")
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}
	// Read just the first row of the target tab. range defaults to the tab
	// the append uses; "<tab>!1:1" pins it to the header row.
	tab := params.StringDefault(job.Params, "range", "Sheet1")
	rng := tab + "!1:1"
	q := url.Values{}
	q.Set("majorDimension", "ROWS")
	endpoint := sheetsBaseURL(job) + "/spreadsheets/" + url.PathEscape(id) + "/values/" + url.PathEscape(rng) + "?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", sheetsErr(body))
	}
	var parsed struct {
		Values [][]any `json:"values"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("values.get decode: %w", err)
	}
	if len(parsed.Values) == 0 {
		return []core.AccountResource{}, nil
	}
	seen := map[string]struct{}{}
	out := make([]core.AccountResource, 0, len(parsed.Values[0]))
	for _, h := range parsed.Values[0] {
		name := strings.TrimSpace(cell(h))
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, core.AccountResource{ID: name, Name: name})
	}
	return out, nil
}

// ListDriveFiles lists the connected account's Drive files of a given
// MIME type (most-recent first) as {id, name} options — the backend for
// the spreadsheet and form pickers (both are Drive file types). Reuses the
// package's Google client + token hook; reads `account`/`timeout_ms`
// from job.Params. The form/spreadsheet ID a caller stores IS the Drive
// file id, so the option ID drops straight into spreadsheet_id / form_id.
func ListDriveFiles(ctx context.Context, job core.Job, mimeType string) ([]core.AccountResource, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("q", "mimeType='"+mimeType+"' and trashed=false")
	q.Set("fields", "files(id,name)")
	q.Set("orderBy", "modifiedTime desc")
	q.Set("pageSize", "100")
	endpoint := driveBaseURL(job) + "/files?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", sheetsErr(body))
	}
	var parsed struct {
		Files []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("drive files.list decode: %w", err)
	}
	out := make([]core.AccountResource, 0, len(parsed.Files))
	for _, f := range parsed.Files {
		out = append(out, core.AccountResource{ID: f.ID, Name: f.Name})
	}
	return out, nil
}

// ListSheetTabs lists the tab (sheet) titles within a spreadsheet as
// {id, name} options — the backend for the tab/range picker, which depends
// on the chosen spreadsheet_id. The tab title is both the id and the label
// (append/read target a tab by name). Reads spreadsheet_id (ID or URL) and
// account/token from job.Params.
func ListSheetTabs(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	id := sheetID(params.StringDefault(job.Params, "spreadsheet_id", ""))
	if id == "" {
		return nil, fmt.Errorf("spreadsheet_id is required")
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("fields", "sheets.properties.title")
	endpoint := sheetsBaseURL(job) + "/spreadsheets/" + url.PathEscape(id) + "?" + q.Encode()
	status, body, err := googleDo(ctx, "GET", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", sheetsErr(body))
	}
	var parsed struct {
		Sheets []struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("spreadsheets.get decode: %w", err)
	}
	out := make([]core.AccountResource, 0, len(parsed.Sheets))
	for _, s := range parsed.Sheets {
		if s.Properties.Title == "" {
			continue
		}
		out = append(out, core.AccountResource{ID: s.Properties.Title, Name: s.Properties.Title})
	}
	return out, nil
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

// RowNumberColumn is the column sheets_read_range adds when asked for row
// numbers, and the one sheets_update_cells reads to know which row to write
// back to. Underscored so it can't collide with a real header.
const RowNumberColumn = "_row"

// firstDataRow is the sheet row number of the first row a read returns.
// Reading a whole tab starts at row 1 (row 2 when the first row is headers);
// an offset cell range (A5:D20) starts wherever it says.
func firstDataRow(cells string, useHeaders bool) int {
	start := 1
	if cells = strings.TrimSpace(cells); cells != "" {
		if m := a1StartRow.FindStringSubmatch(cells); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				start = n
			}
		}
	}
	if useHeaders {
		start++
	}
	return start
}

// a1StartRow pulls the row number out of the left-hand side of an A1 range
// ("B5:D20" → 5). A range with no row number ("B:D") leaves the default.
var a1StartRow = regexp.MustCompile(`^[A-Za-z]*([0-9]+)`)

// columnLetter converts a 0-based column index to its spreadsheet letter
// (0 → A, 25 → Z, 26 → AA).
func columnLetter(i int) string {
	if i < 0 {
		return ""
	}
	out := ""
	for {
		out = string(rune('A'+i%26)) + out
		i = i/26 - 1
		if i < 0 {
			return out
		}
	}
}
