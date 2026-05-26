package sheets

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// fakeSheets stubs the Sheets v4 endpoints we hit. Same pattern as
// the gmail/slack fakes — record the last request, return whatever
// the test configures.
type fakeSheets struct {
	server *httptest.Server

	mu sync.Mutex

	// append
	lastAppendPath  string
	lastAppendQuery string
	lastAppendBody  []byte
	lastAppendAuth  string
	appendResp      string
	appendStatus    int

	// read
	lastReadPath  string
	lastReadQuery string
	lastReadAuth  string
	readResp      string
	readStatus    int
}

func newFakeSheets(t *testing.T) *fakeSheets {
	t.Helper()
	f := &fakeSheets{
		appendStatus: 200,
		appendResp: `{
			"spreadsheetId":"sheet-id-x",
			"updates":{"updatedRange":"Sheet1!A2:C3","updatedRows":2,"updatedColumns":3,"updatedCells":6}
		}`,
		readStatus: 200,
		readResp: `{"range":"Sheet1!A1:C3","majorDimension":"ROWS","values":[
			["name","age","email"],
			["Alice","30","alice@example.com"],
			["Bob","25","bob@example.com"]
		]}`,
	}
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Append URLs end in :append; read URLs don't. URL.Path is
		// already path-escaped by Go's encode, so :append survives as
		// literal "%3Aappend" or as ":append" depending on the http
		// client. We check the un-escaped path.
		path := r.URL.Path
		if strings.HasSuffix(path, ":append") {
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.lastAppendPath = path
			f.lastAppendQuery = r.URL.RawQuery
			f.lastAppendBody = body
			f.lastAppendAuth = r.Header.Get("Authorization")
			status := f.appendStatus
			resp := f.appendResp
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, resp)
			return
		}
		f.mu.Lock()
		f.lastReadPath = path
		f.lastReadQuery = r.URL.RawQuery
		f.lastReadAuth = r.Header.Get("Authorization")
		status := f.readStatus
		resp := f.readResp
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}
	f.server = httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(f.server.Close)
	prev := currentHTTPBase()
	SetHTTPBase(f.server.URL)
	t.Cleanup(func() { SetHTTPBase(prev) })
	return f
}

func installTokenLookup(t *testing.T, fn TokenLookup) {
	t.Helper()
	tokenLookupMu.RLock()
	prev := tokenLookup
	tokenLookupMu.RUnlock()
	SetTokenLookup(fn)
	t.Cleanup(func() { SetTokenLookup(prev) })
}

// ===== sheets_append_row ============================================

func TestSheetsAppendRow_BasicAppend(t *testing.T) {
	fs := newFakeSheets(t)
	res, err := executeSheetsAppendRow(t.Context(), core.Job{
		Params: map[string]any{
			"token":          "ya29.test",
			"spreadsheet_id": "sheet-id-x",
			"range":          "Sheet1",
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"name": "Alice", "age": 30},
				{"name": "Bob", "age": 25},
			}},
			"headers": {Inline: []string{"name", "age"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.lastAppendAuth != "Bearer ya29.test" {
		t.Errorf("auth = %q", fs.lastAppendAuth)
	}
	// Default value_input_option / insert_data_option in the query.
	if !strings.Contains(fs.lastAppendQuery, "valueInputOption=USER_ENTERED") {
		t.Errorf("missing valueInputOption: %q", fs.lastAppendQuery)
	}
	if !strings.Contains(fs.lastAppendQuery, "insertDataOption=INSERT_ROWS") {
		t.Errorf("missing insertDataOption: %q", fs.lastAppendQuery)
	}
	// Body should be {range, majorDimension, values:[[name,age],...]}
	var sent map[string]any
	_ = json.Unmarshal(fs.lastAppendBody, &sent)
	if sent["range"] != "Sheet1" {
		t.Errorf("body.range = %v", sent["range"])
	}
	vals := sent["values"].([]any)
	if len(vals) != 2 {
		t.Fatalf("len(values) = %d, want 2", len(vals))
	}
	row0 := vals[0].([]any)
	if row0[0] != "Alice" {
		t.Errorf("values[0][0] = %v, want Alice", row0[0])
	}
	// JSON encoded int (30) as a number — Sheets parses it as a
	// numeric cell under USER_ENTERED. Float64 here is JSON's
	// universal number type.
	if v, _ := row0[1].(float64); v != 30 {
		t.Errorf("values[0][1] = %v (%T), want 30", row0[1], row0[1])
	}

	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["appended_rows"] != 2 {
		t.Errorf("meta.appended_rows = %v", meta["appended_rows"])
	}
	if meta["updated_range"] != "Sheet1!A2:C3" {
		t.Errorf("meta.updated_range = %v", meta["updated_range"])
	}
}

func TestSheetsAppendRow_HeadersDerivedSortedWhenAbsent(t *testing.T) {
	// No headers input → union of row keys, sorted (same rule as
	// db inserts and excel_write).
	fs := newFakeSheets(t)
	_, _ = executeSheetsAppendRow(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "spreadsheet_id": "s",
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"zebra": "z", "apple": "a"}}},
		},
	}, nil)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fs.lastAppendBody, &sent)
	row := sent["values"].([]any)[0].([]any)
	if row[0] != "a" || row[1] != "z" {
		t.Errorf("derived column order = %v, want [a z]", row)
	}
}

func TestSheetsAppendRow_ShortRowsPaddedToHeaders(t *testing.T) {
	// A row missing a column the headers list includes should land
	// as "" in that cell — same shape excel_write and the SQL
	// inserts produce.
	fs := newFakeSheets(t)
	_, _ = executeSheetsAppendRow(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "spreadsheet_id": "s"},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"name": "Alice"}}},
			"headers": {Inline: []string{"name", "age", "email"}},
		},
	}, nil)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fs.lastAppendBody, &sent)
	row := sent["values"].([]any)[0].([]any)
	if len(row) != 3 || row[0] != "Alice" || row[1] != "" || row[2] != "" {
		t.Errorf("padding broken: %v", row)
	}
}

func TestSheetsAppendRow_EmptyRowsShortCircuits(t *testing.T) {
	// Sheets returns 400 for empty values lists; we'd rather return
	// a clean OK with appended_rows=0 so a graph that filters
	// everything out doesn't fail.
	fs := newFakeSheets(t)
	res, _ := executeSheetsAppendRow(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "spreadsheet_id": "s"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	// And we didn't bother calling Sheets at all.
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.lastAppendBody != nil {
		t.Errorf("empty rows shouldn't hit Sheets API; got body %q", fs.lastAppendBody)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["appended_rows"] != 0 {
		t.Errorf("meta.appended_rows = %v", meta["appended_rows"])
	}
}

func TestSheetsAppendRow_OAuthLookupUsed(t *testing.T) {
	fs := newFakeSheets(t)
	var sawAccount string
	installTokenLookup(t, func(_ context.Context, account string) (string, error) {
		sawAccount = account
		return "ya29.from-oauth", nil
	})
	_, _ = executeSheetsAppendRow(t.Context(), core.Job{
		Params: map[string]any{
			"account": "main", "spreadsheet_id": "s",
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"k": "v"}}},
			"headers": {Inline: []string{"k"}},
		},
	}, nil)
	if sawAccount != "main" {
		t.Errorf("lookup account = %q", sawAccount)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.lastAppendAuth != "Bearer ya29.from-oauth" {
		t.Errorf("auth = %q", fs.lastAppendAuth)
	}
}

func TestSheetsAppendRow_MissingSpreadsheetID(t *testing.T) {
	_ = newFakeSheets(t)
	res, _ := executeSheetsAppendRow(t.Context(), core.Job{
		Params: map[string]any{"token": "x"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"k": "v"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q", res.Status, res.Error.Code)
	}
}

func TestSheetsAppendRow_MissingRowsInput(t *testing.T) {
	_ = newFakeSheets(t)
	res, _ := executeSheetsAppendRow(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "spreadsheet_id": "s"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q", res.Status, res.Error.Code)
	}
}

func TestSheetsAppendRow_GoogleErrorMessageSurfaces(t *testing.T) {
	fs := newFakeSheets(t)
	fs.appendStatus = 403
	fs.appendResp = `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`
	res, _ := executeSheetsAppendRow(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "spreadsheet_id": "s"},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"k": "v"}}},
			"headers": {Inline: []string{"k"}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "sheets_error" {
		t.Fatalf("status=%q code=%q", res.Status, res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "caller does not have permission") {
		t.Errorf("missing Google message: %q", res.Error.Message)
	}
}

// ===== sheets_read_range ============================================

func TestSheetsReadRange_DefaultHeadersFromFirstRow(t *testing.T) {
	fs := newFakeSheets(t)
	res, err := executeSheetsReadRange(t.Context(), core.Job{
		Params: map[string]any{
			"token":          "x",
			"spreadsheet_id": "s",
			"range":          "Sheet1!A1:C3",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	headers := res.Output["headers"].Inline.([]string)
	if !reflect.DeepEqual(headers, []string{"name", "age", "email"}) {
		t.Errorf("headers = %v", headers)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0]["name"] != "Alice" || rows[1]["email"] != "bob@example.com" {
		t.Errorf("rows = %+v", rows)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !strings.Contains(fs.lastReadQuery, "valueRenderOption=FORMATTED_VALUE") {
		t.Errorf("missing default render option: %q", fs.lastReadQuery)
	}
	if !strings.Contains(fs.lastReadQuery, "majorDimension=ROWS") {
		t.Errorf("majorDimension not pinned: %q", fs.lastReadQuery)
	}
}

func TestSheetsReadRange_HeadersFalseSynthesizesNames(t *testing.T) {
	fs := newFakeSheets(t)
	fs.readResp = `{"values":[["Alice","30"],["Bob","25"]]}`
	res, _ := executeSheetsReadRange(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "spreadsheet_id": "s",
			"headers": false,
		},
	}, nil)
	headers := res.Output["headers"].Inline.([]string)
	if !reflect.DeepEqual(headers, []string{"col_0", "col_1"}) {
		t.Errorf("headers = %v", headers)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 || rows[0]["col_0"] != "Alice" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestSheetsReadRange_ShortRowsPadded(t *testing.T) {
	// Sheets trims trailing blank cells per-row — output map should
	// still have every header key set, with "" for missing cells.
	fs := newFakeSheets(t)
	fs.readResp = `{"values":[
		["name","age","city"],
		["Alice"]
	]}`
	res, _ := executeSheetsReadRange(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "spreadsheet_id": "s"},
	}, nil)
	row := res.Output["rows"].Inline.([]map[string]any)[0]
	if row["name"] != "Alice" || row["age"] != "" || row["city"] != "" {
		t.Errorf("padding broken: %+v", row)
	}
}

func TestSheetsReadRange_EmptySheet(t *testing.T) {
	// Sheets omits `values` entirely when the range is empty.
	fs := newFakeSheets(t)
	fs.readResp = `{"range":"Sheet1!A1:Z","majorDimension":"ROWS"}`
	res, _ := executeSheetsReadRange(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "spreadsheet_id": "s"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 0 {
		t.Errorf("rows = %d, want 0", len(rows))
	}
	headers := res.Output["headers"].Inline.([]string)
	if len(headers) != 0 {
		t.Errorf("headers = %v, want empty", headers)
	}
}

func TestSheetsReadRange_UnformattedValueTypesPreserved(t *testing.T) {
	// With value_render_option=UNFORMATTED_VALUE Sheets returns
	// numbers as JSON numbers, booleans as JSON bools — we pass
	// them through.
	fs := newFakeSheets(t)
	fs.readResp = `{"values":[
		["name","score","active"],
		["Alice",9.5,true],
		["Bob",7,false]
	]}`
	res, _ := executeSheetsReadRange(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "spreadsheet_id": "s",
			"value_render_option": "UNFORMATTED_VALUE",
		},
	}, nil)
	rows := res.Output["rows"].Inline.([]map[string]any)
	if v, _ := rows[0]["score"].(float64); v != 9.5 {
		t.Errorf("score = %v (%T), want float64(9.5)", rows[0]["score"], rows[0]["score"])
	}
	if v, _ := rows[0]["active"].(bool); !v {
		t.Errorf("active = %v (%T)", rows[0]["active"], rows[0]["active"])
	}
}

func TestSheetsReadRange_OAuthLookupUsed(t *testing.T) {
	fs := newFakeSheets(t)
	installTokenLookup(t, func(_ context.Context, account string) (string, error) {
		if account != "default" {
			t.Errorf("account = %q", account)
		}
		return "ya29.from-oauth", nil
	})
	_, _ = executeSheetsReadRange(t.Context(), core.Job{
		Params: map[string]any{"spreadsheet_id": "s"},
	}, nil)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.lastReadAuth != "Bearer ya29.from-oauth" {
		t.Errorf("auth = %q", fs.lastReadAuth)
	}
}

func TestSheetsReadRange_GoogleErrorSurfaces(t *testing.T) {
	fs := newFakeSheets(t)
	fs.readStatus = 404
	fs.readResp = `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`
	res, _ := executeSheetsReadRange(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "spreadsheet_id": "missing"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "sheets_error" {
		t.Fatalf("status=%q code=%q", res.Status, res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "not found") {
		t.Errorf("missing Google message: %q", res.Error.Message)
	}
}

// ===== shape compatibility with the rest of the ecosystem ===========

func TestSheetsReadRange_OutputShapeMatchesExcelRead(t *testing.T) {
	// Contract test: sheets_read_range emits the same Inline types
	// as excel_read so a graph can swap one for the other without
	// any downstream changes.
	_ = newFakeSheets(t)
	res, _ := executeSheetsReadRange(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "spreadsheet_id": "s"},
	}, nil)
	if _, ok := res.Output["rows"].Inline.([]map[string]any); !ok {
		t.Errorf("rows Inline = %T, want []map[string]any", res.Output["rows"].Inline)
	}
	if _, ok := res.Output["headers"].Inline.([]string); !ok {
		t.Errorf("headers Inline = %T, want []string", res.Output["headers"].Inline)
	}
}
