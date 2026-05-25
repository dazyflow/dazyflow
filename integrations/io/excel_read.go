package io

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"github.com/xuri/excelize/v2"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "excel_read",
			Version:        "1.0",
			Label:          "Excel read",
			Color:          "#1f7a3f",
			Icon:           "file-spreadsheet",
			BrandLogo:      "/brands/excel.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Excel",
			Tags:           []string{"excel", "xlsx", "spreadsheet", "read"},
			Description:    "Read rows from an .xlsx sheet in the workspace sandbox. First row is treated as headers unless headers=false. Emits rows as a list of {column: value} maps (all values as strings) and the header list separately.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows"},
				{Port: "headers", Label: "Headers"},
			},
			ParamsSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","format":"workspace-path"},"sheet":{"type":"string"},"headers":{"type":"boolean"},"skip":{"type":"integer"}},"required":["path"]}`),
			Idempotent:   true,
		},
		Execute: executeExcelRead,
	})
}

// executeExcelRead opens an .xlsx file from the workspace sandbox and
// flattens one sheet to a row stream. The first non-skipped row is
// taken as headers by default; with headers=false the columns are
// labelled col_0, col_1, … so downstream nodes can still address them
// by name. All cell values are emitted as strings — excelize stores
// numbers and dates as their displayed string form, and we don't
// attempt type inference v1 (the calling graph can cast as needed).
//
// Like file_read, the path is workspace-relative and resolved through
// os.Root, so "../" or absolute paths are rejected as sandbox_escape.
// The file is read through the sandboxed *os.File and handed to
// excelize.OpenReader, which buffers the whole workbook in memory —
// fine for the typical office spreadsheet, but large files (10⁵+
// rows) will want a streaming variant later.
func executeExcelRead(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	path, err := paramString(job.Params, "path")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if job.WorkspaceRoot == "" {
		return errResult(job, "no_sandbox", "excel_read requires a workspace sandbox"), nil
	}
	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		return errResult(job, "sandbox", fmt.Sprintf("open root: %v", err)), nil
	}
	defer root.Close()

	fh, err := root.Open(path)
	if err != nil {
		if isSandboxEscape(err) {
			return errResult(job, "sandbox_escape", fmt.Sprintf("path %q escapes workspace", path)), nil
		}
		return errResult(job, "io", fmt.Sprintf("open %q: %v", path, err)), nil
	}
	defer fh.Close()

	xl, err := excelize.OpenReader(fh)
	if err != nil {
		return errResult(job, "parse", fmt.Sprintf("open xlsx %q: %v", path, err)), nil
	}
	defer xl.Close()

	sheet, _ := paramStringOpt(job.Params, "sheet")
	if sheet == "" {
		sheets := xl.GetSheetList()
		if len(sheets) == 0 {
			return errResult(job, "parse", "workbook has no sheets"), nil
		}
		sheet = sheets[0]
	} else if idx, _ := xl.GetSheetIndex(sheet); idx < 0 {
		return errResult(job, "bad_param", fmt.Sprintf("sheet %q not found", sheet)), nil
	}

	raw, err := xl.GetRows(sheet)
	if err != nil {
		return errResult(job, "parse", fmt.Sprintf("read rows: %v", err)), nil
	}

	if skip, ok := paramInt(job.Params, "skip"); ok && skip > 0 {
		if skip >= len(raw) {
			raw = nil
		} else {
			raw = raw[skip:]
		}
	}

	useHeaders := true
	if b, ok := paramBool(job.Params, "headers"); ok {
		useHeaders = b
	}

	headers, rows := flattenRows(raw, useHeaders)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: rows},
			"headers": {MIME: "application/json", Inline: headers},
		},
	}, nil
}

// flattenRows turns excelize's [][]string into the (headers, rows)
// shape we ship downstream. Short rows (Excel trims trailing empties)
// are padded with "" so every map has the same key set.
func flattenRows(raw [][]string, useHeaders bool) ([]string, []map[string]string) {
	if len(raw) == 0 {
		return []string{}, []map[string]string{}
	}

	var headers []string
	var data [][]string
	if useHeaders {
		headers = raw[0]
		data = raw[1:]
	} else {
		maxCols := 0
		for _, r := range raw {
			if len(r) > maxCols {
				maxCols = len(r)
			}
		}
		headers = make([]string, maxCols)
		for i := range headers {
			headers[i] = fmt.Sprintf("col_%d", i)
		}
		data = raw
	}

	rows := make([]map[string]string, 0, len(data))
	for _, r := range data {
		rec := make(map[string]string, len(headers))
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
