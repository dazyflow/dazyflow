// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

const Scheme = "scratch://"

// WorkspaceScheme is an optional, redundant spelling of "workspace-relative".
// A bare path is the canonical form, but this prefix was in several step
// examples, so flows built by copying one carry it.
//
// It is stripped rather than ignored because ignoring it was silent and wrong:
// nothing resolved the prefix, so "workspace://reports/x.csv" cleaned to
// "workspace:/reports/x.csv" and the step wrote into a directory literally named
// "workspace:", reporting success.
const WorkspaceScheme = "workspace://"

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

// Rel cleans a workspace-relative path and rejects absolute paths and "../"
// escapes, so a caller can safely join the result against a root directory. ""
// normalizes to ".".
//
// Prefer OpenRoot: it is enforced by the kernel-level *os.Root and closes
// symlink traversal too. Rel exists for the callers that cannot use a root
// handle — os/exec's cmd.Dir and go-git demand a real absolute path — and is the
// single definition, because two copies of a security-relevant path cleaner
// drift.
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

// ResolveDir validates rel against root and returns an absolute directory path
// guaranteed to be inside root, with symlinks resolved.
//
// Rel alone is a STRING check: it stops "../etc" but accepts "link/x" where
// "link" is a symlink pointing out of the workspace, because cleaning a path
// never touches the filesystem. Opening through *os.Root makes the kernel refuse
// the traversal, and the returned name comes from the opened handle.
//
// For callers that need a path rather than a handle (cmd.Dir, go-git). The
// directory must already exist; the cleaned relative path is returned too,
// because callers report it back to the user.
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
