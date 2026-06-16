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

	"git.sr.ht/~klahr/hazyflow/core"
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

func TestDownload_RejectsGoogleNativeDoc(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "g1", "name": "Plan", "mimeType": "application/vnd.google-apps.document",
		})
	}))
	defer srv.Close()
	withDriveEnv(t, srv.URL)

	res, err := executeDownload(context.Background(), core.Job{
		ScratchRoot: t.TempDir(),
		Params:      map[string]any{"file_id": "g1"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
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
