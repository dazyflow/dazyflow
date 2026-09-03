// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sandbox resolves a job's persistent-workspace and per-run
// scratch trees down to os.Root handles that confine file access to
// the chosen tree. Drops that read or write files share this helper so
// the scratch:// URL scheme and path-traversal defense behave the same
// way everywhere — io drops, integration drops (gmail, etc.), and any
// future caller that needs to touch sandbox bytes.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

// Scheme marks a sandbox path that lives in the run's ephemeral scratch
// area rather than the persistent workspace. The prefix is preserved in
// output Refs so a downstream node reading the same scratch:// ref
// resolves to the same place. The scratch tree is reclaimed when the
// run finishes (see CleanupPolicy / the dispatcher's scratch reclaim).
const Scheme = "scratch://"

// WorkspaceScheme is an optional, redundant spelling of "workspace-relative".
// A bare path is the canonical form — it is what the editor's workspace-path
// widget writes and what the drops emit in their output refs — but this prefix
// was in several step examples, so flows built by copying one carry it.
//
// It is stripped here rather than ignored because ignoring it was silent and
// wrong: nothing resolved the prefix, so "workspace://reports/x.csv" cleaned
// to the relative path "workspace:/reports/x.csv" and the step wrote a file
// into a directory literally named "workspace:" — reporting success, in a
// place the author never asked for and the Files page shows as junk. Only
// drops/excel stripped it, so the same path worked there and misfired
// everywhere else.
const WorkspaceScheme = "workspace://"

// Resolve picks the root a sandbox path refers to. A scratch:// path
// resolves against the job's per-run ScratchRoot; everything else is
// workspace-relative against the persistent WorkspaceRoot. Returns the
// absolute root directory and the path relative to it.
func Resolve(job core.Job, p string) (root, rel string, err error) {
	if rest, ok := strings.CutPrefix(p, Scheme); ok {
		if job.ScratchRoot == "" {
			return "", "", fmt.Errorf("scratch:// path %q but this run has no scratch root", p)
		}
		return job.ScratchRoot, rest, nil
	}
	if job.WorkspaceRoot == "" {
		return "", "", fmt.Errorf("no workspace sandbox configured")
	}
	return job.WorkspaceRoot, strings.TrimPrefix(p, WorkspaceScheme), nil
}

// OpenRoot resolves p (which may carry the scratch:// scheme) and
// opens its confining *os.Root. The caller closes the returned root and
// operates on rel — os.Root keeps all access inside the chosen root, so
// path traversal can't cross between scratch and the workspace.
func OpenRoot(job core.Job, p string) (root *os.Root, rel string, err error) {
	dir, rel, err := Resolve(job, p)
	if err != nil {
		return nil, "", err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("open root: %w", err)
	}
	return r, rel, nil
}

// IsEscape returns true when err looks like a path-traversal rejection
// from *os.Root. The stdlib doesn't currently expose a sentinel error
// type for this so we string-match the messages it emits.
func IsEscape(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrInvalid) {
		return true
	}
	return containsAny(err.Error(), "path escapes", "outside root", "invalid argument")
}

func containsAny(s string, parts ...string) bool {
	for _, p := range parts {
		for i := 0; i+len(p) <= len(s); i++ {
			if s[i:i+len(p)] == p {
				return true
			}
		}
	}
	return false
}

// Rel cleans a workspace-relative path and rejects absolute paths and
// "../" escapes, so a caller can safely join the result against a root
// directory. "" normalizes to ".".
//
// Prefer OpenRoot: it is enforced by the kernel-level *os.Root and closes
// symlink traversal too. Rel exists for the callers that genuinely cannot
// use a root handle — os/exec's cmd.Dir and go-git both demand a real
// absolute path — and lived as a duplicated copy in the shell and git drops
// until this became the single definition. Two copies of a
// security-relevant path cleaner is exactly the thing that drifts.
func Rel(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return ".", nil
	}
	cleaned := filepath.Clean(rel)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("absolute path %q not allowed", rel)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	return cleaned, nil
}

// ResolveDir validates rel against root and returns an absolute directory
// path that is guaranteed to be inside root — with symlinks resolved.
//
// Rel alone is a STRING check: it stops "../etc" but happily accepts
// "link/x" where "link" is a symlink inside the workspace pointing out of
// it, because cleaning the path never touches the filesystem. Opening the
// path through *os.Root makes the kernel refuse a traversal that leaves
// root, and the returned name comes from the opened handle, so what the
// caller passes to cmd.Dir is the real, verified location.
//
// Intended for callers that need a path rather than a handle (cmd.Dir,
// go-git). The directory must already exist. Returns the absolute directory
// plus the cleaned relative path, which callers report back to the user.
func ResolveDir(root, rel string) (dir, cleanRel string, err error) {
	cleaned, err := Rel(rel)
	if err != nil {
		return "", "", err
	}
	if root == "" {
		return "", "", fmt.Errorf("no workspace sandbox configured")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", "", fmt.Errorf("open root: %w", err)
	}
	defer func() { _ = r.Close() }()
	// Opening the directory through the root is what enforces containment:
	// a symlink escaping the workspace fails here rather than being followed.
	d, err := r.Open(cleaned)
	if err != nil {
		if IsEscape(err) {
			return "", "", fmt.Errorf("path %q escapes workspace", rel)
		}
		return "", "", err
	}
	defer func() { _ = d.Close() }()
	st, err := d.Stat()
	if err != nil {
		return "", "", err
	}
	if !st.IsDir() {
		return "", "", fmt.Errorf("path %q is not a directory", rel)
	}
	return filepath.Join(root, cleaned), cleaned, nil
}
