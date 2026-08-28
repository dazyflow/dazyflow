// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// attachmentServer serves one message whose payload carries an inline logo and
// two real attachments, plus the attachment bodies themselves.
func attachmentServer(t *testing.T) *httptest.Server {
	t.Helper()
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/attachments/att-pdf"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": b64("%PDF-1.4 invoice"), "size": 16})
		case strings.Contains(r.URL.Path, "/attachments/att-csv"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": b64("a,b\n1,2\n"), "size": 8})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "m1",
				"payload": map[string]any{
					"mimeType": "multipart/mixed",
					"parts": []any{
						map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": b64("see attached")}},
						// Inline signature image: no filename, so not an attachment.
						map[string]any{"mimeType": "image/png", "filename": "", "body": map[string]any{"attachmentId": "att-logo", "size": 3}},
						map[string]any{"mimeType": "application/pdf", "filename": "Faktura 2026-08.pdf", "body": map[string]any{"attachmentId": "att-pdf", "size": 16}},
						map[string]any{"mimeType": "text/csv", "filename": "rows.csv", "body": map[string]any{"attachmentId": "att-csv", "size": 8}},
					},
				},
			})
		}
	}))
}

func TestGetAttachments_SavesFilesAndSkipsInline(t *testing.T) {
	srv := attachmentServer(t)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	scratch := t.TempDir()
	res, err := executeGmailGetAttachments(context.Background(), core.Job{
		ID:          "j1",
		Params:      map[string]any{"account": "default", "id": "m1"},
		ScratchRoot: scratch,
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%v error=%+v", res.Status, err, res.Error)
	}

	rows, _ := res.Output["files"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("files = %v, want the two real attachments (inline logo skipped)", res.Output["files"].Inline)
	}
	if rows[0]["name"] != "Faktura 2026-08.pdf" || rows[0]["mime"] != "application/pdf" {
		t.Errorf("first file row = %v", rows[0])
	}
	if got := res.Output["count"].Inline; got != "2" {
		t.Errorf("count = %v, want 2", got)
	}

	// The First file pin is a file ref a filing step can consume directly.
	first := res.Output["first"]
	if !strings.HasPrefix(first.Ref, "scratch://") || first.MIME != "application/pdf" {
		t.Fatalf("first = %+v, want a scratch:// pdf ref", first)
	}
	onDisk := filepath.Join(scratch, strings.TrimPrefix(first.Ref, "scratch://"))
	content, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("attachment not written: %v", err)
	}
	if string(content) != "%PDF-1.4 invoice" {
		t.Errorf("content = %q", content)
	}
	// The saved name must not carry the sender's spaces or path separators.
	if base := filepath.Base(onDisk); strings.ContainsAny(base, " /") {
		t.Errorf("saved name %q is not path-safe", base)
	}
}

func TestGetAttachments_OnlyFilter(t *testing.T) {
	srv := attachmentServer(t)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailGetAttachments(context.Background(), core.Job{
		ID:          "j1",
		Params:      map[string]any{"account": "default", "id": "m1", "only": "pdf"},
		ScratchRoot: t.TempDir(),
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	rows, _ := res.Output["files"].Inline.([]map[string]any)
	if len(rows) != 1 || rows[0]["name"] != "Faktura 2026-08.pdf" {
		t.Fatalf("files = %v, want only the PDF", res.Output["files"].Inline)
	}
}

func TestGetAttachments_NoneIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "m2",
			"payload": map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": ""}},
		})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailGetAttachments(context.Background(), core.Job{
		ID: "j1", Params: map[string]any{"account": "default", "id": "m2"}, ScratchRoot: t.TempDir(),
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	if got := res.Output["count"].Inline; got != "0" {
		t.Errorf("count = %v, want 0", got)
	}
	if _, has := res.Output["first"]; has {
		t.Error("first should be absent when there are no attachments")
	}
}

// A sender-controlled filename must not be able to write outside the sandbox.
func TestGetAttachments_HostileFilename(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/attachments/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": b64("pwned")})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "m3",
			"payload": map[string]any{"parts": []any{
				map[string]any{"mimeType": "application/pdf", "filename": "../../etc/passwd",
					"body": map[string]any{"attachmentId": "att-1", "size": 5}},
			}},
		})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	scratch := t.TempDir()
	res, err := executeGmailGetAttachments(context.Background(), core.Job{
		ID: "j1", Params: map[string]any{"account": "default", "id": "m3"}, ScratchRoot: scratch,
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	first := res.Output["first"]
	if strings.Contains(first.Ref, "..") || strings.Contains(first.Ref, "/etc/") {
		t.Fatalf("traversal survived sanitisation: %q", first.Ref)
	}
	entries, _ := os.ReadDir(scratch)
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file in scratch, got %d", len(entries))
	}
}

func TestGetAttachments_SavesIntoWorkspaceFolder(t *testing.T) {
	srv := attachmentServer(t)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	ws := t.TempDir()
	res, err := executeGmailGetAttachments(context.Background(), core.Job{
		ID:            "j1",
		Params:        map[string]any{"account": "default", "id": "m1", "only": "pdf", "folder": "invoices"},
		WorkspaceRoot: ws,
		ScratchRoot:   t.TempDir(),
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	rows, _ := res.Output["files"].Inline.([]map[string]any)
	saved, _ := rows[0]["path"].(string)
	if !strings.HasPrefix(saved, "invoices/") {
		t.Fatalf("path = %q, want it under the chosen folder", saved)
	}
	if _, err := os.Stat(filepath.Join(ws, saved)); err != nil {
		t.Fatalf("file not written into the workspace: %v", err)
	}
}
