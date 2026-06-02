package excel

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/xuri/excelize/v2"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "excel_read",
			Version:     "1.0",
			Label:       "Excel: read",
			Summary:     "Read rows from an .xlsx workbook in the workspace; the first row is the headers by default.",
			Description: "Read an .xlsx workbook from the workspace into a row stream. The first row becomes the object keys (headers) unless headers:false. Restrict to a cell range (e.g. \"A1:D100\") or skip leading rows; flip on typed mode for native numbers/booleans instead of strings.",
			Integration: "Excel",
			Category:    "io",
			Icon:        "file-spreadsheet",
			BrandLogo:   "/brands/excel.svg",
			Provider:    "internal",
			Tags:        []string{"excel", "xlsx", "spreadsheet", "read"},
			Examples: []core.ParamsExample{
				{Title: "Read a sheet with headers", Params: json.RawMessage(`{"path":"reports/sales.xlsx","sheet":"Sheet1","headers":true}`)},
				{Title: "Typed, skipping a banner", Params: json.RawMessage(`{"path":"exports/q3.xlsx","range":"A3:F500","typed":true}`)},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "path", Label: "Workspace path (overrides params.path when wired)", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":{"type":"string","description":"Workspace-relative path to the .xlsx. Ignored if a 'path' input is wired."},
					"sheet":{"type":"string","description":"Sheet name; defaults to the first sheet."},
					"headers":{"type":"boolean","description":"Treat the first row as headers (default true). False → rows are arrays."},
					"skip":{"type":"integer","description":"Skip this many leading rows before reading."},
					"range":{"type":"string","description":"Cell range, e.g. \"A1:D100\"."},
					"typed":{"type":"boolean","description":"Return native numbers/booleans instead of strings."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeExcelRead,
	})
}

func executeExcelRead(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	path := params.StringDefault(job.Params, "path", "")
	if in, ok := job.Input["path"]; ok && in.Inline != nil {
		path = cellStr(in.Inline)
	}
	path = wsPath(path)
	if path == "" {
		return params.Err(job, "bad_param", "'path' is required"), nil
	}

	data, err := readSandboxFile(job, path)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return params.Err(job, "bad_input", "could not read .xlsx: "+err.Error()), nil
	}
	defer f.Close()

	sheet := params.StringDefault(job.Params, "sheet", "")
	if sheet == "" {
		list := f.GetSheetList()
		if len(list) == 0 {
			return params.Err(job, "no_sheet", "workbook has no sheets"), nil
		}
		sheet = list[0]
	} else if idx, _ := f.GetSheetIndex(sheet); idx == -1 {
		return params.Err(job, "no_sheet", "sheet "+strconv.Quote(sheet)+" not found"), nil
	}

	grid, err := f.GetRows(sheet)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	grid, err = applyRange(grid, params.StringDefault(job.Params, "range", ""), params.IntDefault(job.Params, "skip", 0))
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	typed := params.BoolDefault(job.Params, "typed", false)
	if !params.BoolDefault(job.Params, "headers", true) {
		// header:false → rows are arrays.
		rows := make([]any, 0, len(grid))
		for _, r := range grid {
			arr := make([]any, len(r))
			for i, c := range r {
				arr[i] = coerce(c, typed)
			}
			rows = append(rows, arr)
		}
		return rowsResult(job, rows, []string{}), nil
	}

	if len(grid) == 0 {
		return rowsResult(job, []any{}, []string{}), nil
	}
	headers := append([]string{}, grid[0]...)
	rows := make([]any, 0, len(grid)-1)
	for _, r := range grid[1:] {
		rec := make(map[string]any, len(headers))
		for i, h := range headers {
			if i < len(r) {
				rec[h] = coerce(r[i], typed)
			} else {
				rec[h] = nil
			}
		}
		rows = append(rows, rec)
	}
	return rowsResult(job, rows, headers), nil
}

func rowsResult(job core.Job, rows []any, headers []string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: rows},
			"headers": {MIME: "application/json", Inline: headers},
		},
	}
}

// applyRange narrows the grid to a cell range (e.g. "A1:D100") or, when no
// range is set, drops `skip` leading rows. excelize has no native subgrid
// read, so we slice GetRows ourselves.
func applyRange(grid [][]string, rng string, skip int) ([][]string, error) {
	if rng != "" {
		parts := splitRange(rng)
		if len(parts) != 2 {
			return nil, errBadRange(rng)
		}
		c1, r1, err := excelize.CellNameToCoordinates(parts[0])
		if err != nil {
			return nil, errBadRange(rng)
		}
		c2, r2, err := excelize.CellNameToCoordinates(parts[1])
		if err != nil {
			return nil, errBadRange(rng)
		}
		if r1 > r2 {
			r1, r2 = r2, r1
		}
		if c1 > c2 {
			c1, c2 = c2, c1
		}
		out := [][]string{}
		for r := r1; r <= r2 && r <= len(grid); r++ {
			src := grid[r-1]
			row := make([]string, 0, c2-c1+1)
			for c := c1; c <= c2; c++ {
				if c-1 < len(src) {
					row = append(row, src[c-1])
				} else {
					row = append(row, "")
				}
			}
			out = append(out, row)
		}
		return out, nil
	}
	if skip > 0 {
		if skip >= len(grid) {
			return [][]string{}, nil
		}
		return grid[skip:], nil
	}
	return grid, nil
}

func splitRange(rng string) []string {
	for i := 0; i < len(rng); i++ {
		if rng[i] == ':' {
			return []string{rng[:i], rng[i+1:]}
		}
	}
	return []string{rng}
}

func errBadRange(rng string) error {
	return &rangeError{rng}
}

type rangeError struct{ rng string }

func (e *rangeError) Error() string { return "invalid range " + strconv.Quote(e.rng) }

// coerce returns a typed value (int/float/bool) when typed is on and the
// cell parses cleanly; otherwise the original string. Dates are left as
// excelize's formatted string — community-build parity.
func coerce(s string, typed bool) any {
	if !typed || s == "" {
		return s
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if fl, err := strconv.ParseFloat(s, 64); err == nil {
		return fl
	}
	switch s {
	case "TRUE", "true":
		return true
	case "FALSE", "false":
		return false
	}
	return s
}
