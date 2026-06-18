package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// seedFile writes content into the (tenant, workspace) sandbox at rel,
// creating parent dirs. It writes straight to disk so the file-manager
// endpoints are exercised against real on-disk state.
func seedFile(t *testing.T, root, tenant, workspace, rel, content string) {
	t.Helper()
	full := filepath.Join(root, tenant, workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func fileReq(t *testing.T, h *gatewayHarness, token, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, rdr)
	req.Header.Set("Authorization", "Bearer "+token)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestFiles_ListAndDownload(t *testing.T) {
	h, root := newUploadHarness(t)
	seedFile(t, root, "t", "ws", "report.txt", "hello")
	seedFile(t, root, "t", "ws", "src/main.go", "package main")

	// List the root: one file + one dir, dirs first.
	rw := fileReq(t, h, h.token, "GET", "/api/v1/workspaces/t/ws/files/list", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rw.Code, rw.Body.String())
	}
	var listed struct {
		Path    string      `json:"path"`
		Entries []fileEntry `json:"entries"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(listed.Entries), listed.Entries)
	}
	if !listed.Entries[0].IsDir || listed.Entries[0].Name != "src" {
		t.Errorf("first entry = %+v, want dir 'src'", listed.Entries[0])
	}
	if listed.Entries[1].Name != "report.txt" || listed.Entries[1].Size != 5 {
		t.Errorf("file entry = %+v, want report.txt size 5", listed.Entries[1])
	}

	// Download the file: bytes + attachment headers.
	rw = fileReq(t, h, h.token, "GET", "/api/v1/workspaces/t/ws/files/download?path=report.txt", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("download status=%d", rw.Code)
	}
	if rw.Body.String() != "hello" {
		t.Errorf("body = %q, want hello", rw.Body.String())
	}
	if cd := rw.Header().Get("Content-Disposition"); cd != `attachment; filename="report.txt"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if ct := rw.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want octet-stream (no inline render)", ct)
	}
	if rw.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
}

func TestFiles_ListHidesScratch(t *testing.T) {
	h, root := newUploadHarness(t)
	seedFile(t, root, "t", "ws", "keep.txt", "x")
	seedFile(t, root, "t", "ws", scratchDirName+"/run-1/tmp.bin", "y")

	rw := fileReq(t, h, h.token, "GET", "/api/v1/workspaces/t/ws/files/list", "")
	var listed struct {
		Entries []fileEntry `json:"entries"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &listed)
	for _, e := range listed.Entries {
		if e.Name == scratchDirName {
			t.Fatalf("scratch directory leaked into listing: %+v", listed.Entries)
		}
	}
	if len(listed.Entries) != 1 {
		t.Errorf("got %d entries, want 1 (scratch hidden)", len(listed.Entries))
	}
}

func TestFiles_MkdirRenameDelete(t *testing.T) {
	h, root := newUploadHarness(t)
	seedFile(t, root, "t", "ws", "draft.txt", "v1")

	// mkdir
	if rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/mkdir", `{"path":"archive/2026"}`); rw.Code != http.StatusOK {
		t.Fatalf("mkdir status=%d body=%s", rw.Code, rw.Body.String())
	}
	if fi, err := os.Stat(filepath.Join(root, "t", "ws", "archive", "2026")); err != nil || !fi.IsDir() {
		t.Fatalf("archive/2026 not created: %v", err)
	}

	// rename (moves into the new folder, creating parents as needed)
	if rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/rename", `{"from":"draft.txt","to":"archive/2026/final.txt"}`); rw.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", rw.Code, rw.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "t", "ws", "draft.txt")); !os.IsNotExist(err) {
		t.Errorf("source still present after rename")
	}
	if b, err := os.ReadFile(filepath.Join(root, "t", "ws", "archive", "2026", "final.txt")); err != nil || string(b) != "v1" {
		t.Errorf("moved file = %q err=%v", b, err)
	}

	// delete the whole archive folder
	if rw := fileReq(t, h, h.token, "DELETE", "/api/v1/workspaces/t/ws/files?path=archive", ""); rw.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rw.Code, rw.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "t", "ws", "archive")); !os.IsNotExist(err) {
		t.Errorf("archive still present after delete")
	}
}

// TestFiles_RenameRefusesOverwrite locks in the data-loss guard: a move/rename
// onto an existing path must be rejected, not silently clobber the target.
func TestFiles_RenameRefusesOverwrite(t *testing.T) {
	h, root := newUploadHarness(t)
	seedFile(t, root, "t", "ws", "a.txt", "source")
	seedFile(t, root, "t", "ws", "docs/a.txt", "PRECIOUS")

	rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/rename",
		`{"from":"a.txt","to":"docs/a.txt"}`)
	if rw.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409 Conflict; body=%s", rw.Code, rw.Body.String())
	}
	// The existing destination must be untouched, and the source still present.
	if b, _ := os.ReadFile(filepath.Join(root, "t", "ws", "docs", "a.txt")); string(b) != "PRECIOUS" {
		t.Errorf("destination was clobbered: %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "t", "ws", "a.txt")); err != nil {
		t.Errorf("source went missing: %v", err)
	}
}

func TestFiles_DeleteRootAndScratchRefused(t *testing.T) {
	h, _ := newUploadHarness(t)
	if rw := fileReq(t, h, h.token, "DELETE", "/api/v1/workspaces/t/ws/files", ""); rw.Code != http.StatusBadRequest {
		t.Errorf("delete root status=%d, want 400", rw.Code)
	}
	if rw := fileReq(t, h, h.token, "DELETE", "/api/v1/workspaces/t/ws/files?path="+scratchDirName, ""); rw.Code != http.StatusBadRequest {
		t.Errorf("delete scratch status=%d, want 400", rw.Code)
	}
}

func TestFiles_TraversalRejected(t *testing.T) {
	h, _ := newUploadHarness(t)
	for _, target := range []string{
		"/api/v1/workspaces/t/ws/files/list?path=../../etc",
		"/api/v1/workspaces/t/ws/files/download?path=../secret",
		"/api/v1/workspaces/t/ws/files?path=../../x", // DELETE
	} {
		method := "GET"
		if strings.Contains(target, "/files?") {
			method = "DELETE"
		}
		rw := fileReq(t, h, h.token, method, target, "")
		if rw.Code != http.StatusBadRequest {
			t.Errorf("%s %s status=%d, want 400", method, target, rw.Code)
		}
	}
}

// TestFiles_RequireEdit pins the editor-gated file-manager model: the whole
// surface — reads included — needs graph:edit. A viewer (graph:run only) is
// forbidden across the board; the UI hides Files from them and the server
// enforces it (mutations doubly so). See httpfiles.go's endpoint table.
func TestFiles_RequireEdit(t *testing.T) {
	h, root := newUploadHarness(t)
	seedFile(t, root, "t", "ws", "a.txt", "x")
	role := core.Role{Name: "runner", Permissions: []core.Permission{core.PermGraphRun}}
	_, runnerTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-runner", "t", "ws", "bob", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// graph:run alone is forbidden from every file endpoint — read and mutate.
	cases := []struct {
		name, method, path, body string
	}{
		{"list", "GET", "/api/v1/workspaces/t/ws/files/list", ""},
		{"download", "GET", "/api/v1/workspaces/t/ws/files/download?path=a.txt", ""},
		{"delete", "DELETE", "/api/v1/workspaces/t/ws/files?path=a.txt", ""},
		{"mkdir", "POST", "/api/v1/workspaces/t/ws/files/mkdir", `{"path":"d"}`},
	}
	for _, c := range cases {
		if rw := fileReq(t, h, runnerTok, c.method, c.path, c.body); rw.Code != http.StatusForbidden {
			t.Errorf("runner %s status=%d, want 403", c.name, rw.Code)
		}
	}
}

func TestFiles_Usage(t *testing.T) {
	h, _ := newUploadHarness(t)
	rw := fileReq(t, h, h.token, "GET", "/api/v1/workspaces/t/ws/files/usage", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("usage status=%d", rw.Code)
	}
	var u struct {
		Used  int64 `json:"used"`
		Limit int64 `json:"limit"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// No quota provider wired in the harness ⇒ unlimited (0/0); the point
	// is the endpoint authorizes and returns the shape.
	if u.Limit != 0 {
		t.Errorf("limit = %d, want 0 (no quota provider)", u.Limit)
	}
}
