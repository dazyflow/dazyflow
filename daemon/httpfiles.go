package daemon

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// httpfiles exposes the persistent workspace sandbox (<base>/<tenant>/
// <workspace>/) as a browsable, manageable filesystem — the read/manage
// half of the surface httpupload.go already writes to. All paths are
// confined by os.Root (the same primitive upload uses), so a client path
// can never escape the workspace.
//
// Routes (registered in httpgateway.go):
//
//	GET    …/files/list?path=        list one directory          (graph:edit)
//	GET    …/files/download?path=    download one file           (graph:edit)
//	GET    …/files/usage             tenant byte usage vs limit  (graph:edit)
//	DELETE …/files?path=             delete a file or directory  (graph:edit)
//	POST   …/files/mkdir   {path}    create a directory          (graph:edit)
//	POST   …/files/rename  {from,to} move/rename                 (graph:edit)
//
// The per-run ephemeral scratch directory (.scratch) is internal plumbing
// reclaimed when a run ends, so it is hidden from listings and protected
// from mutation — browsing or deleting it would only confuse users and
// could break in-flight runs.

// fileEntry is one row in a directory listing.
type fileEntry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"` // workspace-relative path to this entry
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// requireWorkspaceEdit runs the shared gate for the workspace-file surface:
// the {tenant}/{workspace} path values must be writable by the principal
// (RequireWorkspace + graph:edit) and the engine sandbox must be configured.
// Returns the resolved (tenant, workspace) and ok=false (after writing the
// error response) when the handler should stop. Shared by the file CRUD
// (via openWorkspaceFS), the upload handler, and the usage endpoint.
func (h *HTTPGateway) requireWorkspaceEdit(rw http.ResponseWriter, r *http.Request, p core.Principal) (tenant, workspace string, ok bool) {
	tenant = r.PathValue("tenant")
	workspace = r.PathValue("workspace")
	if err := core.RequireWorkspace(p, tenant, workspace); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return "", "", false
	}
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return "", "", false
	}
	if h.svc.Engine == nil || h.svc.Engine.Sandbox == nil {
		writeJSONError(rw, http.StatusServiceUnavailable, "workspace sandbox not configured")
		return "", "", false
	}
	return tenant, workspace, true
}

// openWorkspaceFS runs the shared auth + sandbox gate and returns an opened
// os.Root for (tenant, workspace). The caller must Close it. On failure it
// writes the error response and returns ok=false.
func (h *HTTPGateway) openWorkspaceFS(rw http.ResponseWriter, r *http.Request, p core.Principal) (*os.Root, bool) {
	tenant, workspace, ok := h.requireWorkspaceEdit(rw, r, p)
	if !ok {
		return nil, false
	}
	root, err := h.svc.Engine.Sandbox.Root(tenant, workspace)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("sandbox: %v", err))
		return nil, false
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("open sandbox: %v", err))
		return nil, false
	}
	return rootFS, true
}

// cleanWorkspaceRel validates that a client-supplied path is
// workspace-relative and returns it cleaned. An empty path means the
// workspace root ("."). Absolute and parent-escaping paths are rejected
// here for a clear error; os.Root would refuse them anyway.
func cleanWorkspaceRel(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ".", nil
	}
	c := path.Clean(raw)
	if c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") {
		return "", fmt.Errorf("path must be workspace-relative")
	}
	return c, nil
}

// isScratch reports whether rel is the internal scratch directory or
// anything inside it — those are hidden and protected from mutation.
func isScratch(rel string) bool {
	return rel == scratchDirName || strings.HasPrefix(rel, scratchDirName+"/")
}

func (h *HTTPGateway) listWorkspaceFiles(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	rootFS, ok := h.openWorkspaceFS(rw, r, p)
	if !ok {
		return
	}
	defer rootFS.Close()

	rel, err := cleanWorkspaceRel(r.URL.Query().Get("path"))
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	dir, err := rootFS.Open(rel)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, fmt.Sprintf("open %q: %v", rel, err))
		return
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if !info.IsDir() {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("%q is a file, not a directory", rel))
		return
	}
	dirents, err := dir.ReadDir(-1)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("read dir: %v", err))
		return
	}

	entries := make([]fileEntry, 0, len(dirents))
	for _, de := range dirents {
		child := de.Name()
		if rel != "." {
			child = path.Join(rel, de.Name())
		}
		// Hide the internal per-run scratch tree from the listing.
		if isScratch(child) {
			continue
		}
		e := fileEntry{Name: de.Name(), Path: child, IsDir: de.IsDir()}
		if fi, err := de.Info(); err == nil {
			e.Size = fi.Size()
			e.ModTime = fi.ModTime()
		}
		entries = append(entries, e)
	}
	// Directories first, then files, each alphabetical — a predictable
	// order the UI can render without re-sorting.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})

	writeJSON(rw, http.StatusOK, map[string]any{"path": rel, "entries": entries})
}

func (h *HTTPGateway) downloadWorkspaceFile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	rootFS, ok := h.openWorkspaceFS(rw, r, p)
	if !ok {
		return
	}
	defer rootFS.Close()

	rel, err := cleanWorkspaceRel(r.URL.Query().Get("path"))
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if rel == "." {
		writeJSONError(rw, http.StatusBadRequest, "path is required")
		return
	}
	f, err := rootFS.Open(rel)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, fmt.Sprintf("open %q: %v", rel, err))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if info.IsDir() {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("%q is a directory", rel))
		return
	}

	// Workspace files can originate from an arbitrary git_checkout, so they
	// are untrusted: force a download (never inline) with a neutral type and
	// nosniff, so an HTML/SVG file can't execute in the dashboard origin.
	rw.Header().Set("Content-Type", "application/octet-stream")
	rw.Header().Set("X-Content-Type-Options", "nosniff")
	rw.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(path.Base(rel))+"\"")
	rw.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	rw.WriteHeader(http.StatusOK)
	// CopyN, not Copy: bound the body to the size we advertised in
	// Content-Length. A concurrent run can rewrite this file (the workspace
	// is shared with live flows); if it grew, Copy would stream extra bytes
	// past the declared length and corrupt the response.
	_, _ = io.CopyN(rw, f, info.Size())
}

func (h *HTTPGateway) deleteWorkspaceFile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	rootFS, ok := h.openWorkspaceFS(rw, r, p)
	if !ok {
		return
	}
	defer rootFS.Close()

	rel, err := cleanWorkspaceRel(r.URL.Query().Get("path"))
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if rel == "." {
		writeJSONError(rw, http.StatusBadRequest, "refusing to delete the workspace root")
		return
	}
	if isScratch(rel) {
		writeJSONError(rw, http.StatusBadRequest, "the scratch directory is managed automatically")
		return
	}
	// RemoveAll so deleting a folder takes its contents; idempotent on a
	// missing path, so a double-delete is not an error.
	if err := rootFS.RemoveAll(rel); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("delete %q: %v", rel, err))
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"path": rel, "deleted": true})
}

func (h *HTTPGateway) mkdirWorkspaceDir(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	rootFS, ok := h.openWorkspaceFS(rw, r, p)
	if !ok {
		return
	}
	defer rootFS.Close()

	body, ok := decodeRequestJSON[struct {
		Path string `json:"path"`
	}](rw, r)
	if !ok {
		return
	}
	rel, err := cleanWorkspaceRel(body.Path)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if rel == "." || isScratch(rel) {
		writeJSONError(rw, http.StatusBadRequest, "invalid directory name")
		return
	}
	if err := rootFS.MkdirAll(rel, 0o755); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("mkdir %q: %v", rel, err))
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"path": rel, "created": true})
}

func (h *HTTPGateway) renameWorkspaceFile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	rootFS, ok := h.openWorkspaceFS(rw, r, p)
	if !ok {
		return
	}
	defer rootFS.Close()

	body, ok := decodeRequestJSON[struct {
		From string `json:"from"`
		To   string `json:"to"`
	}](rw, r)
	if !ok {
		return
	}
	from, err := cleanWorkspaceRel(body.From)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("from: %v", err))
		return
	}
	to, err := cleanWorkspaceRel(body.To)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("to: %v", err))
		return
	}
	if from == "." || to == "." {
		writeJSONError(rw, http.StatusBadRequest, "cannot rename the workspace root")
		return
	}
	if isScratch(from) || isScratch(to) {
		writeJSONError(rw, http.StatusBadRequest, "the scratch directory is managed automatically")
		return
	}
	if from == to {
		writeJSON(rw, http.StatusOK, map[string]any{"from": from, "to": to})
		return
	}
	// Refuse to clobber an existing destination — os.Root.Rename would
	// silently overwrite it (rename(2) semantics), losing the target's
	// contents with no warning. The caller must delete/rename it first.
	if _, err := rootFS.Stat(to); err == nil {
		writeJSONError(rw, http.StatusConflict, fmt.Sprintf("%q already exists", to))
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("stat %q: %v", to, err))
		return
	}
	// Create the destination's parent if the move targets a new folder, so
	// "report.txt" → "archive/2026/report.txt" works in one call.
	if dir := path.Dir(to); dir != "." {
		if err := rootFS.MkdirAll(dir, 0o755); err != nil {
			writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("mkdir %q: %v", dir, err))
			return
		}
	}
	if err := rootFS.Rename(from, to); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("rename: %v", err))
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"from": from, "to": to})
}

func (h *HTTPGateway) workspaceFileUsage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, _, ok := h.requireWorkspaceEdit(rw, r, p)
	if !ok {
		return
	}
	// Quota is per-tenant. limit==0 means unlimited; used is best-effort and
	// 0 when no quota provider is configured.
	var used, limit int64
	if h.svc.Engine != nil && h.svc.Engine.Quota != nil {
		limit = h.svc.Engine.Quota.Limit(tenant)
		used, _ = h.svc.Engine.Quota.Used(tenant)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"used": used, "limit": limit})
}

// sanitizeFilename strips characters that could break out of the quoted
// Content-Disposition filename (quotes, backslashes, control bytes incl.
// CR/LF for header-injection). The result is only a display hint; the
// actual file is identified by the request path.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '"' || r == '\\' || r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "download"
	}
	return out
}
