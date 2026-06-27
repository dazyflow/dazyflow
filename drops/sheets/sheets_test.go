// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
