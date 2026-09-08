// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// A clone writes to the tenant's own disk, so it is metered like any other
// write — and the check happens AFTER the tree exists, because a repository's
// size is not knowable in advance.

// Checked after the clone, then rolled back if it does not fit.
func checkoutFitsQuota(job core.Job, dst, cleanRel, mode string, sizeBefore int64) (core.Result, bool) {
	if job.QuotaLimit <= 0 {
		return core.Result{}, true
	}
	projected := job.QuotaUsed - sizeBefore + dirSize(dst)
	if projected <= job.QuotaLimit {
		return core.Result{}, true
	}
	if mode == "cloned" {
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

// Regular files only: a symlink's target is not this tenant's usage.
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
