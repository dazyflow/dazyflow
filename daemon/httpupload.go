// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// maxUploadBytes caps a single upload request body. Quota provides the
// per-tenant disk cap; this is a per-request safety net so a hostile
// client can't pin our memory/temp space by streaming forever.
const maxUploadBytes = 200 << 20 // 200 MiB

// uploadReadTimeout is how long this route gets to read its request body,
// replacing the server's global ReadTimeout for the duration of the upload.
//
// That global (see ServeListener) is 30s and — per net/http — covers reading
// the ENTIRE request including the body, so it silently capped uploads at
// whatever fits in 30s: finishing a maxUploadBytes body inside it demands
// ~6.7 MB/s sustained, well past an ordinary uplink. The limit advertised by
// the 413 path below was therefore unreachable, and a large upload died as a
// severed connection rather than any handled error.
//
// 10 minutes clears maxUploadBytes at ~340 KB/s, so the byte ceiling is the
// binding limit again for any realistic connection. It is an absolute
// deadline, not an idle one, which bounds how long a slow client can hold the
// connection; the route is authenticated and workspace-edit-gated before we
// extend anything, so that exposure isn't open to strangers.
const uploadReadTimeout = 10 * time.Minute

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
func (h *filesAPI) uploadWorkspaceFile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.requireWorkspaceEdit(rw, r, p)
	if !ok {
		return
	}

	// Lift the server's global ReadTimeout for this request before touching
	// the body — see uploadReadTimeout. Best-effort: if the writer can't be
	// unwrapped to the connection we log and carry on under the global
	// timeout, which is the pre-existing behaviour rather than a new failure.
	if err := http.NewResponseController(rw).SetReadDeadline(time.Now().Add(uploadReadTimeout)); err != nil {
		h.logger.Printf("upload %s/%s: extend read deadline: %v (large uploads may time out)", tenant, workspace, err)
	}

	r.Body = http.MaxBytesReader(rw, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		// An oversized upload trips MaxBytesReader, which surfaces here as a
		// raw "http: request body too large" string. Translate it into a
		// clean 413 (code payload_too_large) with a human, size-naming
		// message — otherwise the web UI would show the Go internals verbatim
		// to a non-technical user staging a file.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSONError(rw, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("the file is too large — the upload limit is %d MB", maxUploadBytes>>20))
			return
		}
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

	// Quota check before any disk mutation. ParseMultipartForm has already
	// buffered the part (to memory/temp, bounded by maxUploadBytes), so
	// header.Size is the actual byte count, not a client-declared length.
	//
	// Prefer the atomic Reserve path: a bare Used()+size check is a TOCTOU
	// race (two concurrent uploads both read the same stale Used() and both
	// pass, together busting the limit). Reserve counts the bytes as in-flight
	// under a lock; release() frees the reservation once the file has landed
	// (where it then counts toward Used()).
	if h.svc.Engine.Quota != nil {
		if reserver, ok := h.svc.Engine.Quota.(core.QuotaReserver); ok {
			release, err := reserver.Reserve(tenant, header.Size)
			if err != nil {
				if errors.Is(err, core.ErrQuotaExceeded) {
					writeJSONError(rw, http.StatusInsufficientStorage,
						fmt.Sprintf("upload of %d bytes would exceed the tenant storage limit", header.Size))
					return
				}
				writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("quota: %v", err))
				return
			}
			defer release()
		} else if limit := h.svc.Engine.Quota.Limit(tenant); limit > 0 {
			// Provider without atomic reservation: fall back to the snapshot
			// check, but fail CLOSED on a usage-read error — discarding it
			// (used=0) would silently disable the quota.
			used, err := h.svc.Engine.Quota.Used(tenant)
			if err != nil {
				writeJSONError(rw, http.StatusInternalServerError, "could not read storage usage")
				return
			}
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
		if core.IsSandboxEscape(err) {
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
