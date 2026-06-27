// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"fmt"
	"path/filepath"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// sandboxRel cleans rel and rejects absolute paths or "../" escapes so
// callers can safely join it against job.WorkspaceRoot. go-git and
// os/exec both demand absolute paths, so the os.Root sandbox file_read
// uses isn't available here — we validate by hand.
func sandboxRel(rel string) (string, error) {
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

// resolveRevision resolves ref to a commit hash, extending go-git's
// ResolveRevision with the remote-tracking fallback it omits. go-git's
// rev-parse rules never expand a bare branch name to
// refs/remotes/origin/<name> (see RefRevParseRules), so logging or diffing
// a branch you didn't check out — which exists only as a remote-tracking
// ref after a normal clone — would fail. We try the ref as given first
// (commit SHA, tag, local branch, HEAD, HEAD~n, …) and fall back to
// origin/<ref> for the bare-branch case.
func resolveRevision(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	if h, err := repo.ResolveRevision(plumbing.Revision(ref)); err == nil {
		return *h, nil
	}
	if rr, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", ref), true); err == nil {
		return rr.Hash(), nil
	}
	return plumbing.ZeroHash, fmt.Errorf("ref %q not found (no matching branch, tag, or commit)", ref)
}

func emitProgress(ch chan<- core.Progress, job core.Job, pct float64, msg string) {
	if ch == nil {
		return
	}
	select {
	case ch <- core.Progress{JobID: job.ID, NodeID: job.NodeID, Percent: &pct, Message: msg}:
	default:
	}
}
