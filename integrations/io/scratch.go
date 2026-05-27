package io

import (
	"fmt"
	"os"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// scratchScheme marks a sandbox path that lives in the run's ephemeral
// scratch area rather than the persistent workspace. Any file drop that
// resolves paths through openSandboxRoot gets it uniformly — no per-drop
// flag — and because the scheme is preserved in output Refs, a downstream
// node reading the same scratch:// ref resolves to the same place. The
// scratch tree is reclaimed when the run finishes (see CleanupPolicy /
// the dispatcher's scratch reclamation).
const scratchScheme = "scratch://"

// resolveSandbox picks the root a sandbox path refers to. A scratch://
// path resolves against the job's per-run ScratchRoot; everything else is
// workspace-relative against the persistent WorkspaceRoot. Returns the
// absolute root directory and the path relative to it.
func resolveSandbox(job core.Job, p string) (root, rel string, err error) {
	if rest, ok := strings.CutPrefix(p, scratchScheme); ok {
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

// openSandboxRoot resolves p (which may carry the scratch:// scheme) and
// opens its confining os.Root. The caller closes the returned root and
// operates on rel — os.Root keeps all access inside the chosen root, so
// path traversal can't cross between scratch and the workspace.
func openSandboxRoot(job core.Job, p string) (root *os.Root, rel string, err error) {
	dir, rel, err := resolveSandbox(job, p)
	if err != nil {
		return nil, "", err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("open root: %w", err)
	}
	return r, rel, nil
}
