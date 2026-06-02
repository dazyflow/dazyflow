package excel

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/xuri/excelize/v2"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

const xlsxMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "excel_write",
			Version:     "1.0",
			Label:       "Excel: write",
			Summary:     "Serialize a row stream into an .xlsx workbook in the workspace, optionally appending to an existing sheet.",
			Description: "Write a row stream (array of objects) to an .xlsx workbook in the workspace. Wire a 'headers' input to fix column order; otherwise columns are derived from the first row. With append:true, rows are added to an existing sheet of the same name.",
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
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Written path", MIME: []string{xlsxMIME}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","description":"Workspace-relative destination .xlsx."},
					"sheet":{"type":"string","description":"Sheet name (default \"Sheet1\")."},
					"append":{"type":"boolean","description":"Append rows to an existing sheet of the same name instead of overwriting."},
					"autosize":{"type":"boolean","description":"Accepted for compatibility; not applied."},
					"freezeRow":{"type":"integer","description":"Accepted for compatibility; not applied."}
				},
				"required":["path"]
			}`),
			Idempotent: false,
		},
		Execute: executeExcelWrite,
	})
}

func executeExcelWrite(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	path := wsPath(params.StringDefault(job.Params, "path", ""))
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
	if h, ok := job.Input["headers"]; ok && h.Inline != nil {
		headers = normalizeHeaders(h.Inline)
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
		Output: map[string]core.Ref{"out": {MIME: xlsxMIME, Ref: path}},
	}, nil
}
