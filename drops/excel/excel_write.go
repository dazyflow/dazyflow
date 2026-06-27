// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package excel

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/xuri/excelize/v2"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

const xlsxMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "excel_write",
			Version:     "1.0",
			Label:       "Excel",
			Subtitle:    "Write sheet",
			Summary:     "Write rows to an Excel file, matching each row's fields to columns by header.",
			Description: "Write rows to an Excel (.xlsx) file in the workspace. Wire a rows list into the 'Rows' input; columns are taken from the 'Headers' input or derived from the row fields. Turn on 'Add to existing sheet' to add the rows under what's already there instead of starting the file over.",
			Integration: "Excel",
			Category:    "io",
			Icon:        "file-spreadsheet",
			BrandLogo:   "/brands/excel.svg",
			Provider:    "internal",
			Tags:        []string{"excel", "xlsx", "spreadsheet", "write"},
			Examples: []core.ParamsExample{
				{Title: "Write a report", Params: json.RawMessage(`{"path":"reports/sales-2026.xlsx","sheet":"Sales"}`)},
				{Title: "Append to a log", Params: json.RawMessage(`{"path":"logs/audit.xlsx","sheet":"Events","append":true}`)},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				// Named after the param so the card shows an inline editable box
				// (Unreal-style); a wired value overrides the typed one — e.g. a
				// date-stamped filename built by an upstream step, or an Excel
				// read's 'path' output to write back to the same file.
				{Port: "path", Label: "File", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "File", MIME: []string{xlsxMIME}},
				// path is re-emitted as text so another Excel step downstream can
				// target the same file by wire (mirrors sheets append's
				// spreadsheet_id).
				{Port: "path", Label: "File path", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","title":"File","examples":["reports/sales.xlsx"],"description":"Where to save the .xlsx in the workspace. Ignored when a 'File' input is wired."},
					"sheet":{"type":"string","title":"Sheet","default":"Sheet1","description":"The sheet (tab) to write."},
					"append":{"type":"boolean","title":"Add to existing sheet","default":false,"description":"Add the rows under what's already on the sheet instead of replacing it."},
					"autosize":{"type":"boolean","description":"Accepted for compatibility; not applied.","x_advanced":true},
					"freezeRow":{"type":"integer","description":"Accepted for compatibility; not applied.","x_advanced":true}
				},
				"required":["path"]
			}`),
			Idempotent: false,
		},
		Execute: executeExcelWrite,
	})
}

func executeExcelWrite(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	path := params.StringDefault(job.Params, "path", "")
	// The File input overrides the param when wired (same as excel_read).
	if in, ok := job.Input["path"]; ok && in.Inline != nil {
		path = cellStr(in.Inline)
	}
	path = wsPath(path)
	if path == "" {
		return params.Err(job, "bad_param", "'path' is required"), nil
	}
	in, ok := job.Input["rows"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(in.Inline)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	var headers []string
	// Prefer the column order folded onto the rows value itself; fall back to
	// deriving from the row fields.
	if len(in.Headers) > 0 {
		headers = in.Headers
	}
	if headers == nil {
		headers = deriveHeaders(rows)
	}
	sheet := params.StringDefault(job.Params, "sheet", "Sheet1")
	appendMode := params.BoolDefault(job.Params, "append", false)

	var f *excelize.File
	startRow := 1
	writeHeader := true

	if appendMode && sandboxFileExists(job, path) {
		data, err := readSandboxFile(job, path)
		if err != nil {
			return params.Err(job, "bad_input", err.Error()), nil
		}
		f, err = excelize.OpenReader(bytes.NewReader(data))
		if err != nil {
			return params.Err(job, "bad_input", "could not read existing .xlsx: "+err.Error()), nil
		}
		if idx, _ := f.GetSheetIndex(sheet); idx != -1 {
			existing, _ := f.GetRows(sheet)
			startRow = len(existing) + 1
			writeHeader = false // append after the last row, no repeated header
		} else {
			f.NewSheet(sheet)
		}
	} else {
		f = excelize.NewFile()
		// excelize seeds a default "Sheet1"; create our sheet and drop the
		// default when it differs, so the workbook has exactly our sheet.
		if sheet != "Sheet1" {
			f.NewSheet(sheet)
			_ = f.DeleteSheet("Sheet1")
		}
	}
	defer f.Close()

	row := startRow
	if writeHeader {
		hdr := make([]any, len(headers))
		for i, h := range headers {
			hdr[i] = h
		}
		cell, _ := excelize.CoordinatesToCellName(1, row)
		if err := f.SetSheetRow(sheet, cell, &hdr); err != nil {
			return params.Err(job, "write_failed", err.Error()), nil
		}
		row++
	}
	for _, r := range rows {
		rec := make([]any, len(headers))
		for i, h := range headers {
			rec[i] = r[h]
		}
		cell, _ := excelize.CoordinatesToCellName(1, row)
		if err := f.SetSheetRow(sheet, cell, &rec); err != nil {
			return params.Err(job, "write_failed", err.Error()), nil
		}
		row++
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return params.Err(job, "write_failed", err.Error()), nil
	}
	if err := writeSandboxFile(job, path, buf.Bytes()); err != nil {
		return params.Err(job, "sandbox", err.Error()), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out":  {MIME: xlsxMIME, Ref: path},
			"path": {MIME: "text/plain", Inline: path},
		},
	}, nil
}
