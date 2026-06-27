package drive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

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
