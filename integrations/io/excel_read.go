package io

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

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
			Description:    "Read rows from an .xlsx sheet in the workspace sandbox. The path can come from params.path or, for composable flows, from the 'path' input port (wired from file_picker etc.) — when both are present the input port wins. First row is treated as headers unless headers=false. Optional 'range' restricts to a cell rectangle like \"A1:D100\". With typed=true, cell values come back as native types (int64/float64/bool/time.Time) inferred from each cell's stored Excel type; default is all strings (the displayed value).",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "path", Label: "Workspace-relative path (overrides params.path when wired)", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", MIME: []string{"application/json"}},
			},
			// path is no longer strictly required at the schema level —
			// it can arrive via the input port. The Execute function
			// surfaces a clear bad_input error when neither is supplied.
			ParamsSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","format":"workspace-path","description":"Pick a file. Ignored if a 'path' input port is wired."},"sheet":{"type":"string"},"headers":{"type":"boolean"},"skip":{"type":"integer"},"range":{"type":"string","pattern":"^[A-Z]+[0-9]+:[A-Z]+[0-9]+$"},"typed":{"type":"boolean"}}}`),
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
	// Path resolution: wired input wins over params so a composed
	// flow (file_picker → excel_read) doesn't need the user to also
	// edit excel_read's params. Falls back to params.path for the
	// "drop one node and pick a file" flow which is still valid.
	path := pickPath(job, "path")
	if path == "" {
		return errResult(job, "bad_param", "path is required — set params.path or wire the 'path' input port"), nil
	}
	var err error
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

	typed, _ := paramBool(job.Params, "typed")

	// Untyped mode reads the formatted (display) cell value; typed
	// mode reads the raw stored value (so dates come back as Excel
	// serial floats we can convert via ExcelDateToTime, rather than
	// the locale-formatted display string we'd otherwise have to
	// re-parse).
	var raw [][]string
	if typed {
		raw, err = xl.GetRows(sheet, excelize.Options{RawCellValue: true})
	} else {
		raw, err = xl.GetRows(sheet)
	}
	if err != nil {
		return errResult(job, "parse", fmt.Sprintf("read rows: %v", err)), nil
	}

	// Track the sheet coordinates of the upper-left cell of the data
	// block as it shrinks through clip + skip. Typed mode needs this
	// to call GetCellType on the actual sheet cell — once the data is
	// in `raw[i][j]`, we've lost the original (sheet_row, sheet_col)
	// information unless we carry it through.
	sheetCol0, sheetRow0 := 1, 1

	if rangeStr, ok := paramStringOpt(job.Params, "range"); ok && rangeStr != "" {
		clipped, c1, r1, err := clipToRange(raw, rangeStr)
		if err != nil {
			return errResult(job, "bad_param", err.Error()), nil
		}
		raw = clipped
		sheetCol0 = c1
		sheetRow0 = r1
	}

	if skip, ok := paramInt(job.Params, "skip"); ok && skip > 0 {
		if skip >= len(raw) {
			raw = nil
		} else {
			raw = raw[skip:]
		}
		sheetRow0 += skip
	}

	useHeaders := true
	if b, ok := paramBool(job.Params, "headers"); ok {
		useHeaders = b
	}

	var headers []string
	var rowsOut any
	if typed {
		use1904 := false
		if wp, err := xl.GetWorkbookProps(); err == nil && wp.Date1904 != nil {
			use1904 = *wp.Date1904
		}
		var typedRows []map[string]any
		headers, typedRows = flattenRowsTyped(xl, sheet, raw, useHeaders, sheetCol0, sheetRow0, use1904)
		rowsOut = typedRows
	} else {
		var stringRows []map[string]string
		headers, stringRows = flattenRows(raw, useHeaders)
		rowsOut = stringRows
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: rowsOut},
			"headers": {MIME: "application/json", Inline: headers},
		},
	}, nil
}

// clipToRange restricts the raw [][]string to the cells inside the
// "A1:D100"-style rectangle, padding short rows with "" so the
// downstream slice always has exactly (c2 - c1 + 1) columns. Empty
// cells outside the source data become "" — the same shape excelize
// gives us natively for sparse sheets.
//
// Error cases: malformed range syntax, reversed coordinates
// (e.g. "D5:A1"), and non-A1 references (whole-column "A:A" forms)
// all surface as bad_param. We deliberately don't auto-flip reversed
// ranges — that almost always indicates a copy-paste mistake on the
// user side, not an intended request.
func clipToRange(raw [][]string, rangeStr string) ([][]string, int, int, error) {
	from, to, ok := strings.Cut(rangeStr, ":")
	if !ok {
		return nil, 0, 0, fmt.Errorf("range %q: expected form like \"A1:D100\"", rangeStr)
	}
	c1, r1, err := excelize.CellNameToCoordinates(from)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("range start %q: %v", from, err)
	}
	c2, r2, err := excelize.CellNameToCoordinates(to)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("range end %q: %v", to, err)
	}
	if c2 < c1 || r2 < r1 {
		return nil, 0, 0, fmt.Errorf("range %q: end must be at or after start", rangeStr)
	}
	width := c2 - c1 + 1
	out := make([][]string, 0, r2-r1+1)
	for ri := r1 - 1; ri <= r2-1; ri++ {
		row := make([]string, width)
		if ri < len(raw) {
			src := raw[ri]
			for ci := 0; ci < width; ci++ {
				srcCol := c1 - 1 + ci
				if srcCol < len(src) {
					row[ci] = src[srcCol]
				}
			}
		}
		out = append(out, row)
	}
	return out, c1, r1, nil
}

// flattenRowsTyped is the typed analog of flattenRows. For each
// non-empty cell in the data block, it consults excelize for the cell
// type and converts the raw value to the matching Go type:
//
//	CellTypeBool           → bool
//	CellTypeNumber         → int64 if integral, else float64
//	CellTypeDate           → time.Time via ExcelDateToTime (Excel's
//	                         serial-date number → Go time)
//	CellTypeError          → string (the raw error literal "#N/A", …)
//	CellTypeFormula        → best-effort numeric parse of the cached
//	                         result; falls back to string
//	CellType*String/Unset  → string
//
// Headers stay as strings — header rows are always names, never
// typed data. Sheet coordinates (sheetCol0/Row0) are required so
// GetCellType can address the actual sheet cell behind a (row, col)
// position in the post-clip/skip data block.
func flattenRowsTyped(f *excelize.File, sheet string, raw [][]string, useHeaders bool, sheetCol0, sheetRow0 int, use1904 bool) ([]string, []map[string]any) {
	if len(raw) == 0 {
		return []string{}, []map[string]any{}
	}

	var headers []string
	var dataStart int
	if useHeaders {
		headers = raw[0]
		dataStart = 1
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
		dataStart = 0
	}

	rows := make([]map[string]any, 0, len(raw)-dataStart)
	for ri := dataStart; ri < len(raw); ri++ {
		src := raw[ri]
		sheetRow := sheetRow0 + ri
		rec := make(map[string]any, len(headers))
		for ci, h := range headers {
			if ci >= len(src) || src[ci] == "" {
				// Empty cell — nil rather than "" so JSON sees null,
				// which is the right shape for "no value here" in
				// typed mode.
				rec[h] = nil
				continue
			}
			cellName, _ := excelize.CoordinatesToCellName(sheetCol0+ci, sheetRow)
			ct, _ := f.GetCellType(sheet, cellName)
			rec[h] = coerceTypedCell(f, sheet, cellName, src[ci], ct, use1904)
		}
		rows = append(rows, rec)
	}
	return headers, rows
}

// coerceTypedCell converts an Excel cell's raw value (the stored,
// pre-format-application string) to the Go type matching its Excel
// type. Falls back to the raw string when parsing fails — Excel will
// occasionally hand us values that don't fit the declared type
// (e.g. text accidentally typed into a number-formatted column), and
// silently dropping those is worse than passing the string through.
func coerceTypedCell(f *excelize.File, sheet, cellName, raw string, ct excelize.CellType, use1904 bool) any {
	// Date detection: Excel "dates" are floats with a date number
	// format applied — there's no separate Date storage type for the
	// common case. We check the cell's style FIRST for both Number
	// and Unset, so a cell like 2024-03-15 formatted as a date comes
	// back as time.Time even when GetCellType returns CellTypeUnset.
	if (ct == excelize.CellTypeUnset || ct == excelize.CellTypeNumber || ct == excelize.CellTypeDate) &&
		isDateFormattedCell(f, sheet, cellName) {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			if t, err := excelize.ExcelDateToTime(v, use1904); err == nil {
				return t
			}
		}
	}

	switch ct {
	case excelize.CellTypeUnset:
		// Excel writes no `t` attribute on numeric cells — the most
		// common case in real workbooks. Try integer, then float,
		// then bool, then fall through to string. The order matters:
		// "0" should be int64, not bool false.
		if !strings.ContainsAny(raw, ".eE") {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
				return v
			}
		}
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
		switch strings.ToUpper(raw) {
		case "TRUE":
			return true
		case "FALSE":
			return false
		}
		return raw
	case excelize.CellTypeBool:
		switch strings.ToUpper(raw) {
		case "1", "TRUE":
			return true
		case "0", "FALSE":
			return false
		}
		return raw
	case excelize.CellTypeNumber:
		// Try integer first — "42" should not come back as float64(42),
		// downstream code shouldn't have to do the int-vs-float dance.
		if !strings.ContainsAny(raw, ".eE") {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
				return v
			}
		}
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
		return raw
	case excelize.CellTypeDate:
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			if t, err := excelize.ExcelDateToTime(v, use1904); err == nil {
				return t
			}
		}
		return raw
	case excelize.CellTypeFormula:
		// Cached formula value — type isn't reliable. Best effort:
		// number-parse first, fall through to string.
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
		return raw
	}
	// CellTypeError, CellTypeInlineString, CellTypeSharedString —
	// all stay as strings.
	return raw
}

// excelBuiltInDateFmts is the set of built-in number-format IDs that
// Excel treats as date and/or time formats. See OOXML spec §18.8.30.
// We don't try to parse custom NumFmt code strings ("yyyy-mm-dd"
// etc.) — that's a real lexer's job and would land us right in the
// "m means minutes or month?" tar pit. Custom formats stay as
// numbers; the user can post-process if they need them as dates.
var excelBuiltInDateFmts = map[int]struct{}{
	14: {}, 15: {}, 16: {}, 17: {}, 18: {}, 19: {}, 20: {}, 21: {}, 22: {},
	27: {}, 28: {}, 29: {}, 30: {}, 31: {}, 32: {}, 33: {}, 34: {}, 35: {}, 36: {},
	45: {}, 46: {}, 47: {},
	50: {}, 51: {}, 52: {}, 53: {}, 54: {}, 55: {}, 56: {}, 57: {}, 58: {},
	61: {}, 62: {}, 63: {},
}

// isDateFormattedCell asks excelize for the cell's style index, then
// looks up its NumFmt against the built-in date format IDs and falls
// back to scanning the custom format code (style.CustomNumFmt) when
// present. Returns false on any error — we'd rather emit a number
// than block typed reads on a style-lookup hiccup.
func isDateFormattedCell(f *excelize.File, sheet, cellName string) bool {
	idx, err := f.GetCellStyle(sheet, cellName)
	if err != nil || idx == 0 {
		return false
	}
	style, err := f.GetStyle(idx)
	if err != nil || style == nil {
		return false
	}
	if _, ok := excelBuiltInDateFmts[style.NumFmt]; ok {
		return true
	}
	if style.CustomNumFmt != nil && isDateLikeFormat(*style.CustomNumFmt) {
		return true
	}
	return false
}

// isDateLikeFormat decides whether an Excel custom number-format
// string declares a date or time format. It's intentionally a
// CLASSIFIER, not a renderer — we only need yes/no, never to actually
// format a value through the code.
//
// Excel format syntax in a nutshell:
//
//	- Up to four `;`-separated sections (positive ; negative ; zero ; text).
//	  Only the positive section affects "is this a date?".
//	- "..."        : literal text; date chars inside are inert
//	- \X           : escape; the next char is a literal
//	- [Red] [$-409] [Yellow]: color / locale tags; ignored
//	- [h] [mm] [ss]: elapsed-time markers; counts as date/time
//	- y/m/d/h/s    : date/time tokens when outside quotes/escapes
//
// The famous "m is minutes or month" question is moot for *detection*
// — both meanings make it a date/time format. We only need that
// disambiguation when rendering a value, which we don't do.
func isDateLikeFormat(code string) bool {
	if code == "" {
		return false
	}
	code = firstFormatSection(code)
	return scanFormatForDateChars(code)
}

// firstFormatSection returns the substring up to the first ';' that
// isn't inside quotes/brackets/escapes — i.e. the positive-number
// section of the format. Excel uses sections to render different
// signs differently; only the positive one is relevant for date
// detection because dates can't be negative or text-typed.
func firstFormatSection(code string) string {
	var inQuote, inBracket, prevEscape bool
	for i, r := range code {
		if prevEscape {
			prevEscape = false
			continue
		}
		switch {
		case r == '\\':
			prevEscape = true
		case r == '"':
			inQuote = !inQuote
		case inQuote:
			// nothing
		case r == '[':
			inBracket = true
		case r == ']':
			inBracket = false
		case inBracket:
			// nothing
		case r == ';':
			return code[:i]
		}
	}
	return code
}

// scanFormatForDateChars walks the format string looking for date/time
// tokens (y/m/d/h/s) in positions that are "live" — i.e. not inside
// quoted literals, not escaped with \, not inside color/locale/
// condition brackets. Elapsed-time brackets ([h], [mm], [ss]) are an
// exception: they contain only date/time tokens by definition and
// signal a time format on their own.
func scanFormatForDateChars(code string) bool {
	var inQuote, inBracket, prevEscape bool
	var bracketBuf []rune
	for _, r := range code {
		if prevEscape {
			prevEscape = false
			continue
		}
		if r == '\\' {
			prevEscape = true
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if r == '[' {
			inBracket = true
			bracketBuf = bracketBuf[:0]
			continue
		}
		if r == ']' {
			inBracket = false
			if isElapsedTimeBracket(bracketBuf) {
				return true
			}
			continue
		}
		if inBracket {
			bracketBuf = append(bracketBuf, r)
			continue
		}
		switch r {
		case 'y', 'Y', 'm', 'M', 'd', 'D', 'h', 'H', 's', 'S':
			return true
		}
	}
	return false
}

// isElapsedTimeBracket reports whether the bracket's contents look
// like an elapsed-time marker: a non-empty run of only h/m/s chars.
// Colors ([Red]), locales ([$-409]), and conditions ([<100]) all
// fail this check and get dropped.
func isElapsedTimeBracket(buf []rune) bool {
	if len(buf) == 0 {
		return false
	}
	for _, r := range buf {
		switch r {
		case 'h', 'H', 'm', 'M', 's', 'S':
			// ok
		default:
			return false
		}
	}
	return true
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
