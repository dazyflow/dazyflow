package daemon

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// maxUploadBytes caps a single upload request body. Quota provides the
// per-tenant disk cap; this is a per-request safety net so a hostile
// client can't pin our memory/temp space by streaming forever.
const maxUploadBytes = 200 << 20 // 200 MiB

// uploadWorkspaceFile accepts multipart/form-data with one "file" part
// and optional "path" form field, writes the file into the workspace
// sandbox, and returns the workspace-relative path + size.
//
//	POST /api/v1/workspaces/{tenant}/{workspace}/files
//	  Content-Type: multipart/form-data; boundary=…
//	  Form fields:
//	    file (required)  — the file part
//	    path (optional)  — workspace-relative destination; defaults to
//	                       the upload's filename (with directory stripped)
//	  →  200 {"path":"sales.xlsx","size":12345}
//
// Authorization mirrors the graph-edit gate: anyone who can edit a
// graph in this workspace can stage files for it. Cross-tenant writes
// are rejected at RequireWorkspace.
func (h *HTTPGateway) uploadWorkspaceFile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	if err := core.RequireWorkspace(p, tenant, workspace); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	if h.svc.Engine == nil || h.svc.Engine.Sandbox == nil {
		writeJSONError(rw, http.StatusServiceUnavailable, "workspace sandbox not configured")
		return
	}

	r.Body = http.MaxBytesReader(rw, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("parse multipart: %v", err))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("missing 'file' part: %v", err))
		return
	}
	defer file.Close()

	dest := strings.TrimSpace(r.FormValue("path"))
	if dest == "" {
		// Strip any path the browser may have included in the filename
		// (some clients send "C:\\Users\\…\\sales.xlsx"); we want just
		// the leaf.
		dest = filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	}
	if dest == "" || dest == "." || dest == ".." {
		writeJSONError(rw, http.StatusBadRequest, "destination path is empty or unsafe")
		return
	}
	// Canonicalize the destination so a client-supplied "foo/../bar"
	// becomes "bar" before we hand it to os.Root (which would reject
	// the traversal anyway, but Clean gives us a clearer error path).
	dest = path.Clean(dest)
	if strings.HasPrefix(dest, "../") || dest == ".." || strings.HasPrefix(dest, "/") {
		writeJSONError(rw, http.StatusBadRequest, "destination must be workspace-relative")
		return
	}

	root, err := h.svc.Engine.Sandbox.Root(tenant, workspace)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("sandbox: %v", err))
		return
	}

	// Quota check before any disk mutation. header.Size is the
	// client-claimed size — accurate enough for the budget check
	// (the actual write is bounded by maxUploadBytes anyway).
	if h.svc.Engine.Quota != nil {
		if limit := h.svc.Engine.Quota.Limit(tenant); limit > 0 {
			used, _ := h.svc.Engine.Quota.Used(tenant)
			if used+header.Size > limit {
				writeJSONError(rw, http.StatusInsufficientStorage,
					fmt.Sprintf("upload of %d bytes would push tenant past %d (currently %d)",
						header.Size, limit, used))
				return
			}
		}
	}

	rootFS, err := os.OpenRoot(root)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("open sandbox: %v", err))
		return
	}
	defer rootFS.Close()

	// Create parent directories under the sandbox if the destination
	// includes them ("imports/2026/q1/sales.xlsx" is common enough).
	if dir := path.Dir(dest); dir != "." {
		if err := rootFS.MkdirAll(dir, 0o755); err != nil {
			writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("mkdir: %v", err))
			return
		}
	}

	out, err := rootFS.Create(dest)
	if err != nil {
		if isUploadSandboxEscape(err) {
			writeJSONError(rw, http.StatusBadRequest, "destination escapes workspace")
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("create: %v", err))
		return
	}
	defer out.Close()

	written, err := io.Copy(out, file)
	if err != nil {
		// Best-effort cleanup: a partial file is worse than no file.
		_ = rootFS.Remove(dest)
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("write: %v", err))
		return
	}

	writeJSON(rw, http.StatusOK, map[string]any{
		"path": dest,
		"size": written,
	})
}

// isUploadSandboxEscape mirrors drops/io/file_write.go's check;
// kept local to avoid pulling that package into daemon.
func isUploadSandboxEscape(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrInvalid) {
		return true
	}
	msg := err.Error()
	for _, marker := range []string{"path escapes", "outside root", "invalid argument"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
