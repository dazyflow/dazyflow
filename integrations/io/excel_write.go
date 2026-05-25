package io

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"github.com/xuri/excelize/v2"
)

const excelMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "excel_write",
			Version:        "1.0",
			Label:          "Excel write",
			Color:          "#1f7a3f",
			Icon:           "file-spreadsheet",
			BrandLogo:      "/brands/excel.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Excel",
			Tags:           []string{"excel", "xlsx", "spreadsheet", "write"},
			Description:    "Write rows to an .xlsx file in the workspace sandbox. Input 'rows' is a list of {column: value} records; optional 'headers' input fixes column order (otherwise inferred from row keys, sorted). With append=true, opens an existing file (or creates one) and adds rows below the existing data — the file's header row is the source of truth for column placement. Respects per-tenant disk quota.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Written path", MIME: []string{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}},
			},
			ParamsSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","format":"workspace-path"},"sheet":{"type":"string"},"mkdirs":{"type":"boolean"},"autosize":{"type":"boolean"},"freezeRow":{"type":"integer"},"append":{"type":"boolean"}},"required":["path"]}`),
		},
		Execute: executeExcelWrite,
	})
}

// executeExcelWrite serializes the "rows" input port to an .xlsx file
// under the workspace sandbox. Like file_write, the destination is
// resolved through os.Root so "../" can't escape, and the size budget
// (Job.QuotaLimit) is enforced before the file is committed to disk —
// we render the workbook into a bytes.Buffer first and bail out if
// the result would push the tenant past their cap.
//
// Column order: headers come from (in priority order) the "headers"
// input port, or the union of all row keys sorted alphabetically.
// Map iteration order in Go is randomized, so sorted is the only
// deterministic fallback — graphs that care about a specific order
// must wire the "headers" port (excel_read emits one) or pass it
// through a transformer that produces a []string.
func executeExcelWrite(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dest, err := paramString(job.Params, "path")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if job.WorkspaceRoot == "" {
		return errResult(job, "no_sandbox", "excel_write requires a workspace sandbox"), nil
	}
	rowsInput, ok := job.Input["rows"]
	if !ok {
		return errResult(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(rowsInput.Inline)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	var headers []string
	if h, ok := job.Input["headers"]; ok && h.Inline != nil {
		headers, err = normalizeHeaders(h.Inline)
		if err != nil {
			return errResult(job, "bad_input", err.Error()), nil
		}
	}
	if headers == nil {
		headers = deriveHeaders(rows)
	}

	sheet, _ := paramStringOpt(job.Params, "sheet")
	if sheet == "" {
		sheet = "Sheet1"
	}

	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		return errResult(job, "sandbox", fmt.Sprintf("open root: %v", err)), nil
	}
	defer root.Close()

	// Append path: only kicks in when append=true AND the file already
	// exists with the target sheet populated. Otherwise we fall
	// through to the fresh-write path — same as if append wasn't set
	// — so "append" is safe to leave on for recurring graphs that
	// sometimes start from nothing.
	doAppend, _ := paramBool(job.Params, "append")
	var buf *bytes.Buffer
	var oldSize int64
	if doAppend {
		if info, err := root.Stat(dest); err == nil && !info.IsDir() {
			appended, appendBuf, err := renderAppendedXLSX(root, dest, sheet, headers, rows, job.Params)
			if err != nil {
				return errResult(job, "render", err.Error()), nil
			}
			if appended {
				buf = appendBuf
				oldSize = info.Size()
			}
		}
	}
	if buf == nil {
		freshBuf, err := renderXLSX(sheet, headers, rows, job.Params)
		if err != nil {
			return errResult(job, "render", err.Error()), nil
		}
		buf = freshBuf
	}

	// Quota: for an append-overwrite the cost is only the delta;
	// charging the full new size would double-count the bytes that
	// were already on disk and counted in QuotaUsed.
	delta := int64(buf.Len()) - oldSize
	if job.QuotaLimit > 0 && delta > 0 {
		if job.QuotaUsed+delta > job.QuotaLimit {
			return errResult(job, "quota_exceeded",
				fmt.Sprintf("write of %d bytes (delta %d) would push tenant past %d (currently %d)",
					buf.Len(), delta, job.QuotaLimit, job.QuotaUsed)), nil
		}
	}

	if mkdirs, _ := paramBool(job.Params, "mkdirs"); mkdirs {
		if err := root.MkdirAll(path.Dir(dest), 0o755); err != nil {
			if isSandboxEscape(err) {
				return errResult(job, "sandbox_escape", fmt.Sprintf("mkdirs %q escapes workspace", dest)), nil
			}
			return errResult(job, "io", fmt.Sprintf("mkdir: %v", err)), nil
		}
	}

	out, err := root.Create(dest)
	if err != nil {
		if isSandboxEscape(err) {
			return errResult(job, "sandbox_escape", fmt.Sprintf("dest %q escapes workspace", dest)), nil
		}
		return errResult(job, "io", fmt.Sprintf("create %q: %v", dest, err)), nil
	}
	defer out.Close()

	if _, err := out.Write(buf.Bytes()); err != nil {
		return errResult(job, "io", fmt.Sprintf("write %q: %v", dest, err)), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: excelMIME, Ref: dest},
		},
	}, nil
}

// renderAppendedXLSX opens an existing .xlsx through the workspace
// sandbox, appends rows below the last data row of `sheet`, and
// returns the serialized bytes ready to overwrite the file.
//
// Column placement: the file's first row is treated as the source of
// truth for column ordering. Input row keys are looked up against the
// file's headers, so the value for column "name" lands under
// whatever Excel column "name" lives in — regardless of the order
// the upstream node emitted them in. Input keys with no matching
// file header are silently skipped (logging the user's column
// shape would be the alternative; ignored for v1 simplicity).
//
// Returns (appended=false, nil, nil) — i.e. "fall through to fresh
// write" — when the existing file lacks the target sheet or the
// sheet is empty. That makes append safe to leave on for recurring
// graphs that occasionally start from a blank slate.
func renderAppendedXLSX(root *os.Root, dest, sheet string, inputHeaders []string, rows []map[string]any, params map[string]any) (bool, *bytes.Buffer, error) {
	fh, err := root.Open(dest)
	if err != nil {
		if isSandboxEscape(err) {
			return false, nil, fmt.Errorf("path %q escapes workspace", dest)
		}
		return false, nil, fmt.Errorf("open %q: %w", dest, err)
	}
	defer fh.Close()

	f, err := excelize.OpenReader(fh)
	if err != nil {
		return false, nil, fmt.Errorf("parse existing xlsx %q: %w", dest, err)
	}
	defer f.Close()

	// Sheet missing → fall through to fresh-write path. We could
	// auto-create the sheet here, but then "append" silently morphs
	// into "create" which is exactly the surprise users complain
	// about in other tools; the fresh-write path makes it visible
	// (the whole workbook gets rewritten with that sheet only).
	if idx, _ := f.GetSheetIndex(sheet); idx < 0 {
		return false, nil, nil
	}

	existing, err := f.GetRows(sheet)
	if err != nil {
		return false, nil, fmt.Errorf("read existing rows: %w", err)
	}
	if len(existing) == 0 {
		// Empty sheet → no headers to align against. Fall through.
		return false, nil, nil
	}

	fileHeaders := existing[0]
	headerCol := make(map[string]int, len(fileHeaders))
	for i, h := range fileHeaders {
		if h != "" {
			headerCol[h] = i + 1 // 1-based; excelize uses 1-indexed coords
		}
	}

	// Write rows below the last existing row. len(existing) is the
	// 1-based row number of the next empty row.
	startRow := len(existing) + 1
	for r, row := range rows {
		for k, v := range row {
			col, ok := headerCol[k]
			if !ok {
				continue // input column doesn't exist in the file; skip
			}
			if v == nil {
				continue
			}
			cell, _ := excelize.CoordinatesToCellName(col, startRow+r)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return false, nil, fmt.Errorf("write cell %s: %w", cell, err)
			}
		}
	}

	// Apply autosize / freezeRow if requested — same as fresh write,
	// so re-running a graph that toggles these doesn't quietly stop
	// applying them on subsequent appends.
	if freeze, ok := paramInt(params, "freezeRow"); ok && freeze > 0 {
		_ = f.SetPanes(sheet, &excelize.Panes{
			Freeze: true, YSplit: freeze,
			TopLeftCell: fmt.Sprintf("A%d", freeze+1),
			ActivePane:  "bottomLeft",
		})
	}
	_ = inputHeaders // inputHeaders intentionally ignored when appending — file's headers win.

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return false, nil, fmt.Errorf("serialize: %w", err)
	}
	return true, &buf, nil
}

// renderXLSX writes the headers + rows into a fresh workbook and
// returns the serialized bytes. The whole workbook lives in memory
// during this call — fine for typical office spreadsheets, the same
// constraint as excel_read.
func renderXLSX(sheet string, headers []string, rows []map[string]any, params map[string]any) (*bytes.Buffer, error) {
	f := excelize.NewFile()
	defer f.Close()

	// excelize always starts with a sheet named "Sheet1"; rename it
	// rather than creating a second sheet so the workbook only has
	// the one the caller asked for.
	if sheet != "Sheet1" {
		if err := f.SetSheetName("Sheet1", sheet); err != nil {
			return nil, fmt.Errorf("rename sheet: %w", err)
		}
	}

	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return nil, fmt.Errorf("write header %q: %w", h, err)
		}
	}
	for r, row := range rows {
		for c, h := range headers {
			v, ok := row[h]
			if !ok || v == nil {
				continue
			}
			cell, _ := excelize.CoordinatesToCellName(c+1, r+2)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return nil, fmt.Errorf("write cell %s: %w", cell, err)
			}
		}
	}

	if freeze, ok := paramInt(params, "freezeRow"); ok && freeze > 0 {
		if err := f.SetPanes(sheet, &excelize.Panes{
			Freeze:      true,
			YSplit:      freeze,
			TopLeftCell: fmt.Sprintf("A%d", freeze+1),
			ActivePane:  "bottomLeft",
		}); err != nil {
			return nil, fmt.Errorf("freeze pane: %w", err)
		}
	}

	if autosize, _ := paramBool(params, "autosize"); autosize && len(headers) > 0 {
		first, _ := excelize.ColumnNumberToName(1)
		last, _ := excelize.ColumnNumberToName(len(headers))
		// Excelize doesn't actually compute auto-widths; the best we
		// can do without measuring text is a reasonable default that
		// keeps typical content readable.
		if err := f.SetColWidth(sheet, first, last, 18); err != nil {
			return nil, fmt.Errorf("set col width: %w", err)
		}
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("serialize: %w", err)
	}
	return &buf, nil
}

// normalizeRows coerces the input Inline value into []map[string]any
// regardless of which path it took to get here: a native module can
// hand us []map[string]string or []map[string]any directly, while
// anything routed through gRPC or MCP will arrive as []any of
// map[string]any (the JSON-roundtrip shape). A JSON string is also
// accepted as a convenience for hand-built graphs.
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
	case []byte:
		var parsed []map[string]any
		if err := json.Unmarshal(v, &parsed); err != nil {
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

// normalizeHeaders accepts []string (native) or []any of strings
// (post-JSON-roundtrip). Anything else is a wiring mistake.
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

// deriveHeaders builds the column list as the union of all row keys,
// sorted alphabetically — the only stable order we can produce from
// Go maps. Callers that need a specific order must pass it explicitly.
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
