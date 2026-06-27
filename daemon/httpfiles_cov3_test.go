// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"testing"
)

// renameWorkspaceFile validation branches not covered by the happy-path /
// overwrite tests.

func TestRenameFile_DecodeError(t *testing.T) {
	h, _ := newUploadHarness(t)
	rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/rename", "{not json")
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("malformed = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestRenameFile_BadFrom(t *testing.T) {
	h, _ := newUploadHarness(t)
	rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/rename",
		`{"from":"../escape","to":"ok.txt"}`)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("bad from = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestRenameFile_BadTo(t *testing.T) {
	h, root := newUploadHarness(t)
	seedFile(t, root, "t", "ws", "a.txt", "x")
	rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/rename",
		`{"from":"a.txt","to":"../escape"}`)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("bad to = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestRenameFile_RootRefused(t *testing.T) {
	h, _ := newUploadHarness(t)
	// Empty from/to clean to "." (workspace root) — refused.
	rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/rename",
		`{"from":"","to":"x.txt"}`)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("rename root = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestRenameFile_ScratchRefused(t *testing.T) {
	h, _ := newUploadHarness(t)
	rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/rename",
		`{"from":"`+scratchDirName+`/a.txt","to":"b.txt"}`)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("rename scratch = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestRenameFile_SameSourceDest(t *testing.T) {
	h, root := newUploadHarness(t)
	seedFile(t, root, "t", "ws", "a.txt", "x")
	rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/rename",
		`{"from":"a.txt","to":"a.txt"}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("same from/to = %d (%s), want 200 no-op", rw.Code, rw.Body.String())
	}
}

func TestRenameFile_IntoNewFolder(t *testing.T) {
	h, root := newUploadHarness(t)
	seedFile(t, root, "t", "ws", "report.txt", "x")
	rw := fileReq(t, h, h.token, "POST", "/api/v1/workspaces/t/ws/files/rename",
		`{"from":"report.txt","to":"archive/2026/report.txt"}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("rename into new folder = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}
