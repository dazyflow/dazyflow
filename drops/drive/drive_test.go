// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package drive

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func withDriveEnv(t *testing.T, base string) {
	t.Helper()
	SetHTTPBases(base, base)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ya29-" + account, nil })
	t.Cleanup(func() {
		SetHTTPBases(driveAPIBase, driveUploadBase)
		SetTokenLookup(nil)
	})
}

func TestListFiles_BuildsQueryAndNormalizes(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{
				{"id": "f1", "name": "report.pdf", "mimeType": "application/pdf", "size": "1024", "webViewLink": "https://drive/f1"},
			},
		})
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)

	res, err := executeListFiles(context.Background(), core.Job{
		Params: map[string]any{
			"name_contains": "rep",
			"folder_id":     "FID",
			"mime_type":     "application/pdf",
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	for _, want := range []string{"name contains 'rep'", "'FID' in parents", "mimeType = 'application/pdf'", "trashed = false"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	files := res.Output["files"].Inline.([]map[string]any)
	if len(files) != 1 || files[0]["name"] != "report.pdf" || files[0]["web_view_link"] != "https://drive/f1" {
		t.Errorf("files = %+v", files)
	}
	if res.Output["count"].Inline != "1" {
		t.Errorf("count = %v", res.Output["count"].Inline)
	}
}

func TestListFiles_IncludeTrashedDropsTrashedClause(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)

	if _, err := ListFiles(context.Background(), core.Job{
		Params: map[string]any{"include_trashed": true, "name_contains": "x"},
	}); err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if strings.Contains(gotQuery, "trashed") {
		t.Errorf("trashed clause must be omitted when include_trashed=true: %q", gotQuery)
	}
}

func TestDownload_WritesToScratchAndReportsMeta(t *testing.T) {
	const fileBytes = "PDFDATA"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			_, _ = w.Write([]byte(fileBytes))
			return
		}
		// metadata request
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "f1", "name": "Quarterly report.pdf", "mimeType": "application/pdf", "size": "7",
		})
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)

	scratch := t.TempDir()
	res, err := executeDownload(context.Background(), core.Job{
		ScratchRoot: scratch,
		Params:      map[string]any{"file_id": "f1"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	out := res.Output["out"]
	if out.MIME != "application/pdf" || out.Ref != "scratch://Quarterly report.pdf" {
		t.Errorf("out = %+v", out)
	}
	data, err := os.ReadFile(filepath.Join(scratch, "Quarterly report.pdf"))
	if err != nil || string(data) != fileBytes {
		t.Fatalf("read back: data=%q err=%v", data, err)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["bytes"].(int) != len(fileBytes) || meta["name"] != "Quarterly report.pdf" {
		t.Errorf("meta = %+v", meta)
	}
}

// nativeDocServer answers metadata for a Google-editor doc of the given mime
// and serves any /export request with the body, recording the mimeType asked.
func nativeDocServer(t *testing.T, name, mime, exportBody string, gotExportMIME *string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			if gotExportMIME != nil {
				*gotExportMIME = r.URL.Query().Get("mimeType")
			}
			_, _ = w.Write([]byte(exportBody))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "g1", "name": name, "mimeType": mime})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestDownload_ExportsGoogleDocAsPDFByDefault(t *testing.T) {
	const body = "%PDF-1.4 exported"
	var gotMIME string
	srv := nativeDocServer(t, "Plan", "application/vnd.google-apps.document", body, &gotMIME)
	withDriveEnv(t, srv)

	scratch := t.TempDir()
	res, err := executeDownload(context.Background(), core.Job{
		ScratchRoot: scratch,
		Params:      map[string]any{"file_id": "g1"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if gotMIME != "application/pdf" {
		t.Errorf("export mimeType = %q, want application/pdf", gotMIME)
	}
	out := res.Output["out"]
	if out.MIME != "application/pdf" || out.Ref != "scratch://Plan.pdf" {
		t.Errorf("out = %+v", out)
	}
	data, err := os.ReadFile(filepath.Join(scratch, "Plan.pdf"))
	if err != nil || string(data) != body {
		t.Fatalf("read back: data=%q err=%v", data, err)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["format"] != "pdf" || meta["exported_from"] != "application/vnd.google-apps.document" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestDownload_ExportsWithChosenFormat(t *testing.T) {
	var gotMIME string
	srv := nativeDocServer(t, "Plan", "application/vnd.google-apps.document", "DOCXBYTES", &gotMIME)
	withDriveEnv(t, srv)

	scratch := t.TempDir()
	res, err := executeDownload(context.Background(), core.Job{
		ScratchRoot: scratch,
		Params:      map[string]any{"file_id": "g1", "format": "docx"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if gotMIME != exportFormats["docx"].mime {
		t.Errorf("export mimeType = %q, want %q", gotMIME, exportFormats["docx"].mime)
	}
	if out := res.Output["out"]; out.Ref != "scratch://Plan.docx" {
		t.Errorf("out = %+v", out)
	}
}

func TestDownload_RejectsUnsupportedFormatForType(t *testing.T) {
	srv := nativeDocServer(t, "Plan", "application/vnd.google-apps.document", "x", nil)
	withDriveEnv(t, srv)

	res, _ := executeDownload(context.Background(), core.Job{
		ScratchRoot: t.TempDir(),
		Params:      map[string]any{"file_id": "g1", "format": "csv"}, // csv is Sheets-only
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestDownload_RejectsNonExportableType(t *testing.T) {
	srv := nativeDocServer(t, "My Folder", "application/vnd.google-apps.folder", "x", nil)
	withDriveEnv(t, srv)

	res, _ := executeDownload(context.Background(), core.Job{
		ScratchRoot: t.TempDir(),
		Params:      map[string]any{"file_id": "g1"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "not_downloadable" {
		t.Fatalf("want not_downloadable error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestDownload_RequiresFileID(t *testing.T) {
	withDriveEnv(t, "http://unused.invalid")
	res, err := executeDownload(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != core.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
}

func TestUpload_SendsMultipartRelated(t *testing.T) {
	var gotName, gotContent, gotParents string
	var gotUploadType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUploadType = r.URL.Query().Get("uploadType")
		mediaType, p, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/related" {
			t.Errorf("content-type = %q (err %v)", r.Header.Get("Content-Type"), err)
		}
		mr := multipart.NewReader(r.Body, p["boundary"])
		// Part 1: JSON metadata.
		part, err := mr.NextPart()
		if err != nil {
			t.Fatalf("metadata part: %v", err)
		}
		var meta struct {
			Name    string   `json:"name"`
			Parents []string `json:"parents"`
		}
		_ = json.NewDecoder(part).Decode(&meta)
		gotName = meta.Name
		if len(meta.Parents) > 0 {
			gotParents = meta.Parents[0]
		}
		// Part 2: media.
		part2, err := mr.NextPart()
		if err != nil {
			t.Fatalf("media part: %v", err)
		}
		b, _ := io.ReadAll(part2)
		gotContent = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "new1", "name": meta.Name, "webViewLink": "https://drive/new1"})
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "up.txt"), []byte("hello drive"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := executeUpload(context.Background(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "up.txt", "folder_id": "FOLDER"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	if gotUploadType != "multipart" {
		t.Errorf("uploadType = %q", gotUploadType)
	}
	if gotName != "up.txt" || gotContent != "hello drive" || gotParents != "FOLDER" {
		t.Errorf("name=%q content=%q parents=%q", gotName, gotContent, gotParents)
	}
	if res.Output["file_id"].Inline != "new1" || res.Output["web_view_link"].Inline != "https://drive/new1" {
		t.Errorf("outputs = %+v", res.Output)
	}
}

func TestUpload_FromWiredFileRef(t *testing.T) {
	var gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, p, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, p["boundary"])
		part, _ := mr.NextPart()
		var meta struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(part).Decode(&meta)
		gotName = meta.Name
		_, _ = w.Write([]byte(`{"id":"w1"}`))
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)

	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "data.bin"), []byte("x"), 0o644)
	res, err := executeUpload(context.Background(), core.Job{
		WorkspaceRoot: ws,
		// 'in' Ref wins over a (here absent) path param.
		Input: map[string]core.Ref{"in": {Ref: "data.bin"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if gotName != "data.bin" {
		t.Errorf("name = %q, want data.bin (basename of wired ref)", gotName)
	}
}

func TestListFolders_QueryAndProjection(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{{"id": "fold1", "name": "Reports"}},
		})
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)

	got, err := ListFolders(context.Background(), core.Job{Params: map[string]any{}})
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if !strings.Contains(gotQuery, "mimeType = 'application/vnd.google-apps.folder'") || !strings.Contains(gotQuery, "trashed = false") {
		t.Errorf("query = %q", gotQuery)
	}
	if len(got) != 1 || got[0].ID != "fold1" || got[0].Name != "Reports" {
		t.Errorf("options = %+v", got)
	}
}

func TestListFilesForPicker_ExcludesFolders(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		_, _ = w.Write([]byte(`{"files":[{"id":"f1","name":"a.pdf"}]}`))
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)

	got, err := ListFilesForPicker(context.Background(), core.Job{Params: map[string]any{}})
	if err != nil {
		t.Fatalf("ListFilesForPicker: %v", err)
	}
	if !strings.Contains(gotQuery, "mimeType != 'application/vnd.google-apps.folder'") {
		t.Errorf("file picker must exclude folders: %q", gotQuery)
	}
	if len(got) != 1 || got[0].ID != "f1" {
		t.Errorf("options = %+v", got)
	}
}

func TestUpload_RequiresPath(t *testing.T) {
	withDriveEnv(t, "http://unused.invalid")
	res, err := executeUpload(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != core.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
}

// errServer returns a server that always answers with the given status and body
// (a Google {error:{message}} envelope), so non-2xx paths can be exercised.
func errServer_Cov(t *testing.T, status int, msg string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestResolveFileID_Cov(t *testing.T) {
	tests := []struct {
		name string
		job  core.Job
		want string
	}{
		{"param", core.Job{Params: map[string]any{"file_id": " f1 "}}, "f1"},
		{"wired_string", core.Job{Input: map[string]core.Ref{"file_id": {Inline: " w1 "}}}, "w1"},
		{"wired_bytes", core.Job{Input: map[string]core.Ref{"file_id": {Inline: []byte(" b1 ")}}}, "b1"},
		{"wired_empty_falls_to_param", core.Job{
			Input:  map[string]core.Ref{"file_id": {Inline: "  "}},
			Params: map[string]any{"file_id": "p1"},
		}, "p1"},
		{"wired_blank_bytes_falls_to_param", core.Job{
			Input:  map[string]core.Ref{"file_id": {Inline: []byte("   ")}},
			Params: map[string]any{"file_id": "p2"},
		}, "p2"},
		{"wired_nil_inline", core.Job{
			Input:  map[string]core.Ref{"file_id": {Inline: nil}},
			Params: map[string]any{"file_id": "p3"},
		}, "p3"},
		{"wired_wrong_type_falls_to_param", core.Job{
			Input:  map[string]core.Ref{"file_id": {Inline: 123}},
			Params: map[string]any{"file_id": "p4"},
		}, "p4"},
		{"none", core.Job{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveFileID(tc.job); got != tc.want {
				t.Errorf("resolveFileID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSafeBase_Cov(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"report.pdf", "report.pdf"},
		{"  spaced.txt  ", "spaced.txt"},
		{"a/b/c.txt", "c.txt"},
		{`a\b\c.txt`, "c.txt"},
		{"", ""},
		{"   ", ""},
		{"..", ""},
		{".", ""},
		{"/", ""},
	}
	for _, tc := range tests {
		if got := safeBase(tc.in); got != tc.want {
			t.Errorf("safeBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFriendlyNative_Cov(t *testing.T) {
	tests := []struct {
		mime, want string
	}{
		{"application/vnd.google-apps.folder", "Drive folder"},
		{"application/vnd.google-apps.form", "Google Form"},
		{"application/vnd.google-apps.site", "Google Site"},
		{"application/vnd.google-apps.shortcut", "Drive shortcut"},
		{"application/vnd.google-apps.unknown", "Google-editor file (application/vnd.google-apps.unknown)"},
	}
	for _, tc := range tests {
		if got := friendlyNative(tc.mime); got != tc.want {
			t.Errorf("friendlyNative(%q) = %q, want %q", tc.mime, got, tc.want)
		}
	}
}

func TestQuoteDriveValue_Cov(t *testing.T) {
	if got := quoteDriveValue(`a'b\c`); got != `a\'b\\c` {
		t.Errorf("quoteDriveValue = %q", got)
	}
}

func TestDriveAPIError_Cov(t *testing.T) {
	e := &driveAPIError{msg: "boom"}
	if e.Error() != "boom" {
		t.Errorf("Error() = %q", e.Error())
	}
}

func TestBaseURL_ParamOverride_Cov(t *testing.T) {
	withDriveEnv(t, "http://seam.invalid")
	job := core.Job{Params: map[string]any{"base_url": "http://api.example", "upload_url": "http://up.example"}}
	if got := apiBaseURL(job); got != "http://api.example" {
		t.Errorf("apiBaseURL = %q", got)
	}
	if got := uploadBaseURL(job); got != "http://up.example" {
		t.Errorf("uploadBaseURL = %q", got)
	}
	// Fallback to the seam when no override.
	if got := apiBaseURL(core.Job{}); got != "http://seam.invalid" {
		t.Errorf("apiBaseURL fallback = %q", got)
	}
	if got := uploadBaseURL(core.Job{}); got != "http://seam.invalid" {
		t.Errorf("uploadBaseURL fallback = %q", got)
	}
}

func TestListFiles_APIErrorStatus_Cov(t *testing.T) {
	withDriveEnv(t, errServer_Cov(t, http.StatusForbidden, "no access"))
	res, err := executeListFiles(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || !strings.Contains(res.Error.Message, "no access") {
		t.Fatalf("want drive_error with message, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestListFiles_BadJSON_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)
	_, err := ListFiles(context.Background(), core.Job{Params: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("want decode error, got %v", err)
	}
}

func TestListFiles_TokenError_Cov(t *testing.T) {
	SetHTTPBases("http://unused.invalid", "http://unused.invalid")
	SetTokenLookup(nil)
	t.Cleanup(func() {
		SetHTTPBases(driveAPIBase, driveUploadBase)
		SetTokenLookup(nil)
	})
	res, err := executeListFiles(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("want error status, got %q", res.Status)
	}
}

func TestDownload_MetadataErrorStatus_Cov(t *testing.T) {
	withDriveEnv(t, errServer_Cov(t, http.StatusNotFound, "file gone"))
	res, _ := executeDownload(context.Background(), core.Job{
		ScratchRoot: t.TempDir(),
		Params:      map[string]any{"file_id": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "drive_error" {
		t.Fatalf("want drive_error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestDownload_MediaErrorStatus_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"media boom"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "f1", "name": "a.pdf", "mimeType": "application/pdf"})
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)
	res, _ := executeDownload(context.Background(), core.Job{
		ScratchRoot: t.TempDir(),
		Params:      map[string]any{"file_id": "f1"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || !strings.Contains(res.Error.Message, "media boom") {
		t.Fatalf("want media error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestDownload_ExportErrorStatus_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"export boom"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "g1", "name": "Doc", "mimeType": "application/vnd.google-apps.document"})
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)
	res, _ := executeDownload(context.Background(), core.Job{
		ScratchRoot: t.TempDir(),
		Params:      map[string]any{"file_id": "g1"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || !strings.Contains(res.Error.Message, "export boom") {
		t.Fatalf("want export error, got status=%q err=%+v", res.Status, res.Error)
	}
}

// Default-extension append: a Sheet exported as xlsx whose name lacks the ext
// gets ".xlsx" appended (exercises exportDestPath append branch).
func TestDownload_ExportAppendsExtension_Cov(t *testing.T) {
	srv := nativeDocServer(t, "Budget", "application/vnd.google-apps.spreadsheet", "XLSXBYTES", nil)
	withDriveEnv(t, srv)
	scratch := t.TempDir()
	res, err := executeDownload(context.Background(), core.Job{
		ScratchRoot: scratch,
		Params:      map[string]any{"file_id": "g1", "format": "xlsx"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if out := res.Output["out"]; out.Ref != "scratch://Budget.xlsx" {
		t.Errorf("out = %+v", out)
	}
}

// An explicit 'path' that already carries the extension is not doubled.
func TestDownload_ExportPathKeepsExtension_Cov(t *testing.T) {
	srv := nativeDocServer(t, "Doc", "application/vnd.google-apps.document", "PDF", nil)
	withDriveEnv(t, srv)
	res, err := executeDownload(context.Background(), core.Job{
		ScratchRoot: t.TempDir(),
		Params:      map[string]any{"file_id": "g1", "path": "out.pdf"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if out := res.Output["out"]; out.Ref != "scratch://out.pdf" {
		t.Errorf("out = %+v", out)
	}
}

// A file with no name falls back to "drive-<id>" as the dest base.
func TestDownload_NamelessUsesIDFallback_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") == "media" {
			_, _ = w.Write([]byte("DATA"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "f9", "name": "", "mimeType": "application/octet-stream"})
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)
	res, err := executeDownload(context.Background(), core.Job{
		ScratchRoot: t.TempDir(),
		Params:      map[string]any{"file_id": "f9"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if out := res.Output["out"]; out.Ref != "scratch://drive-f9" {
		t.Errorf("out = %+v", out)
	}
}

func TestUpload_APIErrorStatus_Cov(t *testing.T) {
	withDriveEnv(t, errServer_Cov(t, http.StatusForbidden, "upload denied"))
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "f.txt"), []byte("x"), 0o644)
	res, _ := executeUpload(context.Background(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "f.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || !strings.Contains(res.Error.Message, "upload denied") {
		t.Fatalf("want upload error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestUpload_BadJSONResponse_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{broken"))
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "f.txt"), []byte("x"), 0o644)
	res, _ := executeUpload(context.Background(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "f.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "drive_error" {
		t.Fatalf("want drive_error decode, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestUpload_OpenMissingFile_Cov(t *testing.T) {
	withDriveEnv(t, "http://unused.invalid")
	res, _ := executeUpload(context.Background(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"path": "nope.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "io" {
		t.Fatalf("want io error, got status=%q err=%+v", res.Status, res.Error)
	}
}

// Upload with an explicit name + mime override, verifying buildRelatedBody puts
// the chosen mime on the media part.
func TestUpload_NameAndMimeOverride_Cov(t *testing.T) {
	var gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Content-Type"), "multipart/related") {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		if !strings.Contains(string(body[:n]), "application/custom") {
			t.Errorf("body missing custom mime: %q", body[:n])
		}
		if !strings.Contains(string(body[:n]), `"Renamed"`) {
			gotName = "missing"
		}
		_, _ = w.Write([]byte(`{"id":"c1","name":"Renamed"}`))
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "f.txt"), []byte("hello"), 0o644)
	res, err := executeUpload(context.Background(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "f.txt", "name": "Renamed", "mime_type": "application/custom"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if gotName == "missing" {
		t.Errorf("metadata name not sent")
	}
	if res.Output["file_id"].Inline != "c1" {
		t.Errorf("file_id = %v", res.Output["file_id"].Inline)
	}
}

func TestBuildRelatedBody_Cov(t *testing.T) {
	body, ct, err := buildRelatedBody(map[string]any{"name": "x"}, "text/plain", []byte("data"))
	if err != nil {
		t.Fatalf("buildRelatedBody: %v", err)
	}
	if !strings.HasPrefix(ct, "multipart/related; boundary=") {
		t.Errorf("content-type = %q", ct)
	}
	s := string(body)
	if !strings.Contains(s, "application/json") || !strings.Contains(s, "text/plain") || !strings.Contains(s, "data") {
		t.Errorf("body = %q", s)
	}
	// Empty mime: media part header carries no Content-Type.
	body2, _, err := buildRelatedBody(map[string]any{"name": "y"}, "", []byte("d2"))
	if err != nil {
		t.Fatalf("buildRelatedBody empty mime: %v", err)
	}
	if !strings.Contains(string(body2), "d2") {
		t.Errorf("body2 = %q", body2)
	}
}

func TestBuildQuery_AllFilters_Cov(t *testing.T) {
	q := buildQuery(core.Job{Params: map[string]any{
		"name_contains":   "rep",
		"folder_id":       "FID",
		"mime_type":       "application/pdf",
		"query":           "starred = true",
		"include_trashed": false,
	}})
	for _, want := range []string{
		"name contains 'rep'", "'FID' in parents", "mimeType = 'application/pdf'",
		"trashed = false", "(starred = true)",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
}

func TestDownload_AuthError_Cov(t *testing.T) {
	SetHTTPBases("http://unused.invalid", "http://unused.invalid")
	SetTokenLookup(nil)
	t.Cleanup(func() {
		SetHTTPBases(driveAPIBase, driveUploadBase)
		SetTokenLookup(nil)
	})
	res, err := executeDownload(context.Background(), core.Job{
		ScratchRoot: t.TempDir(),
		Params:      map[string]any{"file_id": "f1"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "auth" {
		t.Fatalf("want auth error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestUpload_AuthError_Cov(t *testing.T) {
	SetHTTPBases("http://unused.invalid", "http://unused.invalid")
	SetTokenLookup(nil)
	t.Cleanup(func() {
		SetHTTPBases(driveAPIBase, driveUploadBase)
		SetTokenLookup(nil)
	})
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "f.txt"), []byte("x"), 0o644)
	res, _ := executeUpload(context.Background(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "f.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "auth" {
		t.Fatalf("want auth error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestListForPicker_APIError_Cov(t *testing.T) {
	withDriveEnv(t, errServer_Cov(t, http.StatusForbidden, "picker denied"))
	if _, err := ListFolders(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatalf("want error")
	}
	if _, err := ListFilesForPicker(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatalf("want error")
	}
}

func TestListForPicker_BadJSON_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{nope"))
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)
	if _, err := ListFolders(context.Background(), core.Job{Params: map[string]any{}}); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("want decode error, got %v", err)
	}
}
