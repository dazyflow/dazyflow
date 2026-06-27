// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Scheme marks a sandbox path that lives in the run's ephemeral scratch
// area rather than the persistent workspace. The prefix is preserved in
// output Refs so a downstream node reading the same scratch:// ref
// resolves to the same place. The scratch tree is reclaimed when the
// run finishes (see CleanupPolicy / the dispatcher's scratch reclaim).
const Scheme = "scratch://"

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
	return job.WorkspaceRoot, p, nil
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
