// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
)

// Disk-quota enforcement for git_checkout.
//
// git_checkout writes an unbounded amount of data into the workspace — the
// remote decides how big the repository is — and it was for a long time the
// one filesystem-touching drop that ignored the tenant budget completely.
// The clone landed whatever its size, and the resulting over-quota state
// then surfaced as a quota_exceeded failure on the NEXT file_write /
// http_download: a node that had done nothing wrong reported the error
// while the node that actually ate the disk reported success.
//
// Why this looks different from file_write's enforcement: there, the write
// size is known before the first byte hits the disk, so the drop takes an
// atomic reservation (io.SetQuotaReserver) across the write. Here the size
// is only knowable after the transfer, so a reservation has nothing to
// reserve. We bound the damage instead, in two parts:
//
//   - a pre-flight refusal, so an org already at its limit never starts a
//     clone at all; and
//   - a post-transfer measure-and-rollback, so a clone that overshot is
//     undone rather than left wedging the org.
//
// This leaves the same window file_write's snapshot path has: two
// concurrent checkouts in one org can both pass the pre-flight and both
// land. Accepted — it bounds the overshoot to one repo per concurrent run
// instead of being unbounded, and closing it properly needs a size the
// protocol won't give us up front.

// checkoutFitsQuota reports whether the tree openOrClone just produced at
// dst fits the tenant's remaining budget, and returns the error Result to
// surface when it does not.
//
// sizeBefore is what this checkout occupied at job start. Those bytes are
// already inside job.QuotaUsed (the engine's snapshot), so a re-run has to
// subtract them before adding the post-fetch size — otherwise the existing
// clone is counted twice and a fetch that added nothing would look like it
// doubled the org's usage. It is zero for a fresh clone.
//
// On overshoot the rollback is deliberately asymmetric:
//
//   - mode "cloned": remove the tree. We created the folder in this job,
//     nothing else references it yet, and leaving a quota-busting clone
//     behind would wedge every later write in the org.
//   - mode "pulled": keep it. The tree pre-existed and was within budget;
//     deleting a cache the user owns because a fetch overshot is hostile,
//     and it would only make the next run re-clone the full repo and fail
//     again, burning the bandwidth every time. The org stays over budget
//     until someone acts, so the message names the folder to delete.
func checkoutFitsQuota(job core.Job, dst, cleanRel, mode string, sizeBefore int64) (core.Result, bool) {
	if job.QuotaLimit <= 0 {
		return core.Result{}, true
	}
	projected := job.QuotaUsed - sizeBefore + dirSize(dst)
	if projected <= job.QuotaLimit {
		return core.Result{}, true
	}
	if mode == "cloned" {
		// Best-effort: a failed rollback must not mask the quota error,
		// which is the actionable half for the user.
		_ = os.RemoveAll(dst)
		return params.Err(job, "quota_exceeded", fmt.Sprintf(
			"the checkout would put this organization at %d bytes, past its %d-byte storage limit; "+
				"the clone was removed — free space, raise the limit, or set a clone depth to fetch less history",
			projected, job.QuotaLimit)), false
	}
	return params.Err(job, "quota_exceeded", fmt.Sprintf(
		"updating this checkout put the organization at %d bytes, past its %d-byte storage limit; "+
			"the existing clone in %q was kept — delete it from the workspace files or raise the limit",
		projected, job.QuotaLimit, cleanRel)), false
}

// dirSize sums the regular files under root, returning 0 when root does
// not exist. It mirrors the daemon's walkUsage accounting (regular files
// only, apparent size) on purpose: this number is compared against
// job.QuotaUsed, which that walk produced, so the two have to agree or the
// projection drifts. A walk error yields the bytes counted so far —
// under-counting a tree we can't fully read is the same direction the
// quota walk itself errs.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}
