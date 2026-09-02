// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"fmt"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

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
