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

// Quota bounds the total; this bounds one request.
const maxUploadBytes = 200 << 20 // 200 MiB

// Lifted above the server's global ReadTimeout for this route only: a large
// upload on a slow link is legitimate, and the global timeout would cut it.
const uploadReadTimeout = 10 * time.Minute

// One "file" part; anything else is refused rather than partially read.
func (h *filesAPI) uploadWorkspaceFile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.requireWorkspaceEdit(rw, r, p)
	if !ok {
		return
	}

	// Before touching the body, or the deadline has already been armed.
	if err := http.NewResponseController(rw).SetReadDeadline(time.Now().Add(uploadReadTimeout)); err != nil {
		h.logger.Printf("upload %s/%s: extend read deadline: %v (large uploads may time out)", tenant, workspace, err)
	}

	r.Body = http.MaxBytesReader(rw, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		// MaxBytesReader surfaces an oversized body here, not as a read error.
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
		dest = filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	}
	if dest == "" || dest == "." || dest == ".." {
		writeJSONError(rw, http.StatusBadRequest, "destination path is empty or unsafe")
		return
	}
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
		_ = rootFS.Remove(dest)
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("write: %v", err))
		return
	}

	writeJSON(rw, http.StatusOK, map[string]any{
		"path": dest,
		"size": written,
	})
}
