// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sheets

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func withSheetsEnv(t *testing.T, base string) {
	t.Helper()
	SetHTTPBases(base, base)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ya29-" + account, nil })
	t.Cleanup(func() {
		SetHTTPBases(sheetsAPIBase, driveAPIBase)
		SetTokenLookup(nil)
	})
}

func TestSheetID_FromURL(t *testing.T) {
	got := sheetID("https://docs.google.com/spreadsheets/d/ABC-123_xy/edit#gid=0")
	if got != "ABC-123_xy" {
		t.Errorf("got %q", got)
	}
	if sheetID("PLAINID") != "PLAINID" {
		t.Errorf("plain id should pass through")
	}
}

func TestSheetsRead_FlattensWithHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"values": [][]any{
				{"name", "email"},
				{"Ada", "ada@x"},
				{"Bo", "bo@y"},
			},
		})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	res, err := executeSheetsRead(context.Background(), core.Job{
		Params: map[string]any{"spreadsheet_id": "S1", "range": "A1:B3"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 || rows[0]["name"] != "Ada" || rows[1]["email"] != "bo@y" {
		t.Errorf("rows = %+v", rows)
	}
	// Column order now rides on the rows Ref's Headers (the former separate
	// "headers" output port was removed when row order was folded onto the Ref).
	headers := res.Output["rows"].Headers
	if strings.Join(headers, ",") != "name,email" {
		t.Errorf("headers = %v", headers)
	}
}

func TestSheetsAppend_MapsRowsToColumns(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, ":append") {
			t.Errorf("path = %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		_ = json.NewEncoder(w).Encode(map[string]any{"updates": map[string]any{"updatedRows": 2, "updatedRange": "Sheet1!A1:B2"}})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	res, err := executeSheetsAppend(context.Background(), core.Job{
		Params: map[string]any{"spreadsheet_id": "S1"},
		Input: map[string]core.Ref{
			// Column order now rides on the rows Ref's Headers (the separate
			// "headers" input port was removed when row order was folded on).
			"rows": {Inline: []map[string]any{{"name": "Ada", "email": "a@x"}, {"name": "Bo", "email": "b@y"}}, Headers: []string{"name", "email"}},
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	values := sent["values"].([]any)
	if len(values) != 2 {
		t.Fatalf("values = %+v", values)
	}
	first := values[0].([]any)
	if first[0] != "Ada" || first[1] != "a@x" {
		t.Errorf("first row = %+v", first)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["appended_rows"] != 2 {
		t.Errorf("meta = %+v", meta)
	}
}

// Nested objects/lists in a row (e.g. for_each Failed rows entries) must not
// fail the append — they're written as JSON text; scalars stay scalars.
func TestSheetsAppend_StringifiesNestedValues(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		_ = json.NewEncoder(w).Encode(map[string]any{"updates": map[string]any{"updatedRows": 1}})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	res, err := executeSheetsAppend(context.Background(), core.Job{
		Params: map[string]any{"spreadsheet_id": "S1"},
		Input: map[string]core.Ref{
			// Column order now rides on the rows Ref's Headers (the separate
			// "headers" input port was removed when row order was folded on).
			"rows": {Inline: []map[string]any{{
				"row":   2,
				"data":  map[string]any{"Email": "b@y", "Name": "Bo"},
				"error": map[string]any{"code": "auth"},
			}}, Headers: []string{"row", "data", "error"}},
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	first := sent["values"].([]any)[0].([]any)
	if first[0] != float64(2) {
		t.Errorf("scalar cell = %v (%T), want number 2 kept as-is", first[0], first[0])
	}
	dataCell, _ := first[1].(string)
	if !strings.Contains(dataCell, `"Name":"Bo"`) {
		t.Errorf("nested cell = %v, want JSON text", first[1])
	}
}

func TestListDriveFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/files") {
			t.Errorf("path = %q", r.URL.Path)
		}
		if q := r.URL.Query().Get("q"); !strings.Contains(q, "vnd.google-apps.spreadsheet") {
			t.Errorf("query missing mimeType filter: %q", q)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{
				{"id": "S1", "name": "Q2 Leads"},
				{"id": "S2", "name": "Inbox Log"},
			},
		})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	got, err := ListDriveFiles(context.Background(),
		core.Job{Params: map[string]any{"account": "default"}},
		"application/vnd.google-apps.spreadsheet")
	if err != nil {
		t.Fatalf("ListDriveFiles: %v", err)
	}
	if len(got) != 2 || got[0].ID != "S1" || got[0].Name != "Q2 Leads" {
		t.Errorf("options = %+v", got)
	}
}

func TestListSheetTabs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/spreadsheets/S1") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sheets": []map[string]any{
				{"properties": map[string]any{"title": "Inbox"}},
				{"properties": map[string]any{"title": "Archive"}},
			},
		})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	got, err := ListSheetTabs(context.Background(),
		core.Job{Params: map[string]any{"account": "default", "spreadsheet_id": "S1"}})
	if err != nil {
		t.Fatalf("ListSheetTabs: %v", err)
	}
	if len(got) != 2 || got[0].ID != "Inbox" || got[0].Name != "Inbox" || got[1].ID != "Archive" {
		t.Errorf("tabs = %+v", got)
	}

	// No spreadsheet_id → error (the dependent picker surfaces this as 502
	// and prompts the user to pick a spreadsheet first).
	if _, err := ListSheetTabs(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Error("missing spreadsheet_id should error")
	}
}

func TestSheetsAppend_MappingProjectsAndOrdersColumns(t *testing.T) {
	// A Google Form response keyed by question title, mapped to differently
	// named sheet columns in an explicit order. The mapping must override
	// the 'headers' input, rename/reorder, and blank a missing source.
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		_ = json.NewEncoder(w).Encode(map[string]any{"updates": map[string]any{"updatedRows": 2}})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	res, err := executeSheetsAppend(context.Background(), core.Job{
		Params: map[string]any{
			"spreadsheet_id": "S1",
			"mapping": []any{
				map[string]any{"column": "Email", "source": "Email Address"},
				map[string]any{"column": "Name", "source": "Full Name"},
				map[string]any{"column": "Notes", "source": "Missing"},
			},
		},
		Input: map[string]core.Ref{
			// Wrong order + an ignored 'headers' input on purpose.
			"headers": {Inline: []any{"Full Name", "Email Address"}},
			"rows": {Inline: []map[string]any{
				{"Full Name": "Ada", "Email Address": "a@x"},
				{"Full Name": "Bo", "Email Address": "b@y"},
			}},
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	values := sent["values"].([]any)
	if len(values) != 2 {
		t.Fatalf("values = %+v", values)
	}
	first := values[0].([]any)
	// Column order Email, Name, Notes — projected from the mapped sources.
	if first[0] != "a@x" || first[1] != "Ada" || first[2] != "" {
		t.Errorf("first row = %+v (want [a@x Ada \"\"])", first)
	}
}

func TestSheetsAppend_MissingRowsInput(t *testing.T) {
	withSheetsEnv(t, "http://unused")
	res, _ := executeSheetsAppend(context.Background(), core.Job{
		Params: map[string]any{"spreadsheet_id": "S1"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestSheetsExportPDF_WritesToScratch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/export") || r.URL.Query().Get("mimeType") != "application/pdf" {
			t.Errorf("export req: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("%PDF-1.4 fake pdf bytes"))
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	scratch := t.TempDir()
	res, err := executeSheetsExportPDF(context.Background(), core.Job{
		Params:      map[string]any{"spreadsheet_id": "S1"},
		ScratchRoot: scratch,
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	pdfRef := res.Output["pdf"]
	if pdfRef.MIME != "application/pdf" || !strings.HasPrefix(pdfRef.Ref, "scratch://") {
		t.Errorf("pdf ref = %+v", pdfRef)
	}
	// The file actually landed in the scratch tree.
	written, err := os.ReadFile(scratch + "/sheet-S1.pdf")
	if err != nil || !strings.HasPrefix(string(written), "%PDF") {
		t.Errorf("scratch file: %v / %q", err, string(written))
	}
}

// 'path' is a friendly file name: a bare "Svar" lands in scratch as
// "Svar.pdf" (scheme + extension added); an explicit scratch:// path from an
// older flow still passes through.
func TestSheetsExportPDF_FriendlyFileName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("%PDF-1.4 fake"))
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	for _, tc := range []struct{ path, wantFile string }{
		{"Svar", "Svar.pdf"},
		{"Survey results.pdf", "Survey results.pdf"},
		{"scratch://legacy.pdf", "legacy.pdf"},
	} {
		scratch := t.TempDir()
		res, err := executeSheetsExportPDF(context.Background(), core.Job{
			Params:      map[string]any{"spreadsheet_id": "S1", "path": tc.path},
			ScratchRoot: scratch,
		}, nil)
		if err != nil || res.Status != core.StatusOK {
			t.Fatalf("path %q: status=%q err=%+v", tc.path, res.Status, res.Error)
		}
		if _, err := os.Stat(scratch + "/" + tc.wantFile); err != nil {
			t.Errorf("path %q: expected file %q: %v", tc.path, tc.wantFile, err)
		}
	}
}

// --- pure helpers (no HTTP) ------------------------------------------------

func TestNormalizeRows_AllForms(t *testing.T) {
	// []map passes through.
	if got, err := normalizeRows([]map[string]any{{"a": 1}}); err != nil || len(got) != 1 {
		t.Errorf("[]map: %v %v", got, err)
	}
	// nil → nil.
	if got, err := normalizeRows(nil); err != nil || got != nil {
		t.Errorf("nil: %v %v", got, err)
	}
	// []any of objects.
	got, err := normalizeRows([]any{map[string]any{"a": 1}, map[string]any{"b": 2}})
	if err != nil || len(got) != 2 {
		t.Errorf("[]any: %v %v", got, err)
	}
	// []any with a non-object element → error.
	if _, err := normalizeRows([]any{"oops"}); err == nil {
		t.Error("[]any non-object should error")
	}
	// single object → one-row slice.
	if got, err := normalizeRows(map[string]any{"a": 1}); err != nil || len(got) != 1 {
		t.Errorf("single obj: %v %v", got, err)
	}
	// JSON string.
	if got, err := normalizeRows(`[{"a":1}]`); err != nil || len(got) != 1 {
		t.Errorf("json string: %v %v", got, err)
	}
	// empty string → nil.
	if got, err := normalizeRows(""); err != nil || got != nil {
		t.Errorf("empty string: %v %v", got, err)
	}
	// invalid JSON string → error.
	if _, err := normalizeRows("{not json"); err == nil {
		t.Error("invalid json should error")
	}
	// unsupported type → error.
	if _, err := normalizeRows(42); err == nil {
		t.Error("int should be unsupported")
	}
}

func TestDeriveHeaders_SortedUnion(t *testing.T) {
	got := deriveHeaders([]map[string]any{
		{"name": "Ada", "email": "a@x"},
		{"email": "b@y", "age": 7},
	})
	// Union of keys, sorted: age, email, name.
	if len(got) != 3 || got[0] != "age" || got[1] != "email" || got[2] != "name" {
		t.Errorf("headers = %v", got)
	}
}

func TestNormalizeHeaders_Forms(t *testing.T) {
	if got := normalizeHeaders([]string{"a", "b"}); len(got) != 2 || got[0] != "a" {
		t.Errorf("[]string: %v", got)
	}
	got := normalizeHeaders([]any{"a", 2, true})
	if len(got) != 3 || got[0] != "a" || got[1] != "2" || got[2] != "true" {
		t.Errorf("[]any: %v", got)
	}
	if got := normalizeHeaders(42); got != nil {
		t.Errorf("unsupported should be nil: %v", got)
	}
}

func TestCell_Coercions(t *testing.T) {
	if cell(nil) != "" {
		t.Error("nil cell")
	}
	if cell("hi") != "hi" {
		t.Error("string cell")
	}
	if cell(7) != "7" {
		t.Error("int cell")
	}
	if cell(true) != "true" {
		t.Error("bool cell")
	}
}

func TestFlattenValues_NoHeaders(t *testing.T) {
	// Ragged rows, headers=false → col_0.. and every row is data.
	headers, rows := flattenValues([][]any{{"a", "b", "c"}, {"x"}}, false)
	if len(headers) != 3 || headers[0] != "col_0" || headers[2] != "col_2" {
		t.Errorf("headers = %v", headers)
	}
	if len(rows) != 2 || rows[0]["col_1"] != "b" || rows[1]["col_2"] != "" {
		t.Errorf("rows = %v", rows)
	}
	// Empty matrix.
	h, r := flattenValues(nil, true)
	if len(h) != 0 || len(r) != 0 {
		t.Errorf("empty = %v / %v", h, r)
	}
}

func TestParseMapping_Forms(t *testing.T) {
	// Missing / nil → nil.
	if parseMapping(map[string]any{}) != nil {
		t.Error("missing mapping should be nil")
	}
	if parseMapping(map[string]any{"mapping": nil}) != nil {
		t.Error("nil mapping should be nil")
	}
	// Empty JSON string → nil.
	if parseMapping(map[string]any{"mapping": ""}) != nil {
		t.Error("empty string mapping should be nil")
	}
	// Invalid JSON string → nil (swallowed).
	if parseMapping(map[string]any{"mapping": "{nope"}) != nil {
		t.Error("invalid json mapping should be nil")
	}
	// JSON string form.
	got := parseMapping(map[string]any{"mapping": `[{"column":"Email","source":"e"}]`})
	if len(got) != 1 || got[0].Column != "Email" || got[0].Source != "e" {
		t.Errorf("json string mapping = %v", got)
	}
	// Non-array, non-string → nil.
	if parseMapping(map[string]any{"mapping": 5}) != nil {
		t.Error("numeric mapping should be nil")
	}
	// Array with a non-object entry and a column-less entry both skipped.
	got = parseMapping(map[string]any{"mapping": []any{
		"junk",
		map[string]any{"source": "x"}, // no column → skip
		map[string]any{"column": "C", "source": "s"},
	}})
	if len(got) != 1 || got[0].Column != "C" {
		t.Errorf("array mapping = %v", got)
	}
}

func TestResolveSpreadsheetID_InputAndParam(t *testing.T) {
	// String input port wins, URL trimmed to id.
	id := resolveSpreadsheetID(core.Job{
		Input: map[string]core.Ref{"spreadsheet_id": {Inline: "https://docs.google.com/spreadsheets/d/WIRED/edit"}},
	})
	if id != "WIRED" {
		t.Errorf("string input = %q", id)
	}
	// []byte input port.
	id = resolveSpreadsheetID(core.Job{
		Input: map[string]core.Ref{"spreadsheet_id": {Inline: []byte("BYTEID")}},
	})
	if id != "BYTEID" {
		t.Errorf("byte input = %q", id)
	}
	// Blank input falls back to param.
	id = resolveSpreadsheetID(core.Job{
		Input:  map[string]core.Ref{"spreadsheet_id": {Inline: "   "}},
		Params: map[string]any{"spreadsheet_id": "PARAMID"},
	})
	if id != "PARAMID" {
		t.Errorf("fallback = %q", id)
	}
	// No input at all → param.
	id = resolveSpreadsheetID(core.Job{Params: map[string]any{"spreadsheet_id": "P2"}})
	if id != "P2" {
		t.Errorf("param only = %q", id)
	}
}

func TestSheetsErr_Message(t *testing.T) {
	msg := sheetsErr([]byte(`{"error":{"message":"boom","status":"PERMISSION_DENIED"}}`))
	if msg == "" {
		t.Errorf("sheetsErr empty for %q", msg)
	}
}

// --- HTTP-backed helpers ---------------------------------------------------

func TestListSheetColumns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"values": [][]any{{"Name", "Email", "", "Name", "Notes"}},
		})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	got, err := ListSheetColumns(context.Background(),
		core.Job{Params: map[string]any{"account": "default", "spreadsheet_id": "S1", "range": "Inbox"}})
	if err != nil {
		t.Fatalf("ListSheetColumns: %v", err)
	}
	// Blank skipped, duplicate "Name" deduped → Name, Email, Notes.
	if len(got) != 3 || got[0].ID != "Name" || got[1].ID != "Email" || got[2].ID != "Notes" {
		t.Errorf("columns = %+v", got)
	}

	// Missing spreadsheet_id → error before HTTP.
	if _, err := ListSheetColumns(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Error("missing spreadsheet_id should error")
	}
}

func TestListSheetColumns_EmptyHeaderRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"values": [][]any{}})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	got, err := ListSheetColumns(context.Background(),
		core.Job{Params: map[string]any{"spreadsheet_id": "S1"}})
	if err != nil || len(got) != 0 {
		t.Errorf("empty header row: %v / %+v", err, got)
	}
}

func TestListSheetColumns_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"message":"no access"}}`))
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	if _, err := ListSheetColumns(context.Background(),
		core.Job{Params: map[string]any{"spreadsheet_id": "S1"}}); err == nil {
		t.Error("403 should error")
	}
}

func TestListDriveFiles_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"drive down"}}`))
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	if _, err := ListDriveFiles(context.Background(),
		core.Job{Params: map[string]any{"account": "default"}}, "application/vnd.google-apps.spreadsheet"); err == nil {
		t.Error("500 should error")
	}
}

func TestListSheetTabs_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	if _, err := ListSheetTabs(context.Background(),
		core.Job{Params: map[string]any{"spreadsheet_id": "S1"}}); err == nil {
		t.Error("404 should error")
	}
}

// --- execute-path error/edge branches --------------------------------------

func TestSheetsRead_MissingID(t *testing.T) {
	withSheetsEnv(t, "http://unused")
	res, _ := executeSheetsRead(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("res = %+v", res)
	}
}

func TestSheetsRead_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"bad range"}}`))
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)
	res, _ := executeSheetsRead(context.Background(),
		core.Job{Params: map[string]any{"spreadsheet_id": "S1"}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "sheets_error" {
		t.Errorf("res = %+v", res)
	}
}

func TestSheetsRead_CellsRangeAndNoHeaders(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"values": [][]any{{"x", "y"}, {"1", "2"}}})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	res, err := executeSheetsRead(context.Background(), core.Job{
		Params: map[string]any{"spreadsheet_id": "S1", "range": "Inbox Log", "cells": "A1:B2", "headers": false},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	// Quoted tab + cells made it into the path.
	if gotPath == "" {
		t.Error("no request path captured")
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	// headers=false → both rows are data keyed col_0/col_1.
	if len(rows) != 2 || rows[0]["col_0"] != "x" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestSheetsAppend_MissingID(t *testing.T) {
	withSheetsEnv(t, "http://unused")
	res, _ := executeSheetsAppend(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("res = %+v", res)
	}
}

func TestSheetsAppend_EmptyRowsEmitsZeroPorts(t *testing.T) {
	withSheetsEnv(t, "http://unused")
	res, err := executeSheetsAppend(context.Background(), core.Job{
		Params: map[string]any{"spreadsheet_id": "S1"},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{}}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if res.Output["appended_rows"].Inline != "0" || res.Output["updated_range"].Inline != "" {
		t.Errorf("zero-row outputs = %+v", res.Output)
	}
}

func TestSheetsAppend_BadRowsInput(t *testing.T) {
	withSheetsEnv(t, "http://unused")
	res, _ := executeSheetsAppend(context.Background(), core.Job{
		Params: map[string]any{"spreadsheet_id": "S1"},
		Input:  map[string]core.Ref{"rows": {Inline: 99}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("res = %+v", res)
	}
}

func TestSheetsAppend_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"message":"denied"}}`))
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)
	res, _ := executeSheetsAppend(context.Background(), core.Job{
		Params: map[string]any{"spreadsheet_id": "S1"},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": 1}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "sheets_error" {
		t.Errorf("res = %+v", res)
	}
}

// A mapping that introduces a NEW column triggers the header-row PUT before
// the append — exercising the writeHeaderCols branch (and readSheetHeaders).
func TestSheetsAppend_MappingWritesNewHeader(t *testing.T) {
	var putHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET": // readSheetHeaders — existing row 1
			_ = json.NewEncoder(w).Encode(map[string]any{"values": [][]any{{"Email"}}})
		case r.Method == "PUT": // header (re)write for the new column
			putHeader = true
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default: // POST append
			_ = json.NewEncoder(w).Encode(map[string]any{"updates": map[string]any{"updatedRows": 1}})
		}
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	res, err := executeSheetsAppend(context.Background(), core.Job{
		Params: map[string]any{
			"spreadsheet_id": "S1",
			"mapping": []any{
				map[string]any{"column": "Email", "source": "e"},
				map[string]any{"column": "Name", "source": "n"}, // new column
			},
		},
		Input: map[string]core.Ref{"rows": {Inline: []map[string]any{{"e": "a@x", "n": "Ada"}}}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if !putHeader {
		t.Error("expected a header-row PUT for the new column")
	}
}

func TestSheetsAppend_MappingReadHeadersError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":{"message":"hdr read fail"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	res, _ := executeSheetsAppend(context.Background(), core.Job{
		Params: map[string]any{
			"spreadsheet_id": "S1",
			"mapping":        []any{map[string]any{"column": "Name", "source": "n"}},
		},
		Input: map[string]core.Ref{"rows": {Inline: []map[string]any{{"n": "Ada"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "sheets_error" {
		t.Errorf("res = %+v", res)
	}
}

// base_url param override is honored over the package base (used like `token`
// by the integration tests). Pointing it at a server proves the override path.
func TestBaseURLParamOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sheets": []map[string]any{
			{"properties": map[string]any{"title": "Tab1"}},
		}})
	}))
	defer srv.Close()
	// Package base points elsewhere; the per-job base_url must win.
	withSheetsEnv(t, "http://unused.invalid")
	got, err := ListSheetTabs(context.Background(), core.Job{
		Params: map[string]any{"spreadsheet_id": "S1", "base_url": srv.URL},
	})
	if err != nil || len(got) != 1 || got[0].ID != "Tab1" {
		t.Errorf("base_url override: %v / %+v", err, got)
	}
}

// driveBaseURL override (export hits Drive).
func TestDriveBaseURLParamOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("%PDF-1.4 ok"))
	}))
	defer srv.Close()
	withSheetsEnv(t, "http://unused.invalid")
	res, err := executeSheetsExportPDF(context.Background(), core.Job{
		Params:      map[string]any{"spreadsheet_id": "S1", "base_url": srv.URL},
		ScratchRoot: t.TempDir(),
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
}

// lookupField walks dotted paths and returns "" for absent/non-object steps.
func TestLookupField_DottedPaths(t *testing.T) {
	row := map[string]any{"user": map[string]any{"email": "a@x"}, "flat": "v"}
	if lookupField(row, "user.email") != "a@x" {
		t.Error("nested lookup")
	}
	if lookupField(row, "flat") != "v" {
		t.Error("flat lookup")
	}
	if lookupField(row, "") != "" {
		t.Error("empty source → blank")
	}
	if lookupField(row, "user.missing") != "" {
		t.Error("missing leaf → blank")
	}
	if lookupField(row, "flat.deeper") != "" {
		t.Error("descend into non-object → blank")
	}
}

// cellValue stringifies non-scalar values as JSON, scalars pass through.
func TestCellValue_NestedAndScalar(t *testing.T) {
	if cellValue(7) != 7 {
		t.Error("int scalar passes through")
	}
	got, _ := cellValue(map[string]any{"k": "v"}).(string)
	if got == "" {
		t.Errorf("nested map should marshal to JSON text, got %v", got)
	}
}

func TestSheetsExportPDF_MissingID(t *testing.T) {
	withSheetsEnv(t, "http://unused")
	res, _ := executeSheetsExportPDF(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("res = %+v", res)
	}
}

func TestSheetsExportPDF_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"message":"no such file"}}`))
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)
	res, _ := executeSheetsExportPDF(context.Background(), core.Job{
		Params:      map[string]any{"spreadsheet_id": "S1"},
		ScratchRoot: t.TempDir(),
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "sheets_error" {
		t.Errorf("res = %+v", res)
	}
}
