// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"path"
	"strings"
)

// GitCacheDirName is the workspace subdirectory that holds git_checkout
// clones — one per (flow, node). The folder is auto-derived (the drop has
// no folder param), persists across runs as a cache (a re-run fetches +
// resets instead of re-cloning), and is removed when the flow is deleted.
// It is NOT hidden — it appears in the workspace Files browser like any
// other folder.
const GitCacheDirName = "gitcache"

// gitCacheSeg sanitizes a path segment (flow id, node id) so it can't
// escape the cache root via separators or "..". IDs are slugs in practice;
// this is defense in depth.
func gitCacheSeg(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return s
}

// GitCheckoutRel is the workspace-relative checkout path for one node:
// gitcache/<flow>/<node>. Stable across runs of the same flow so the clone
// is reused as a cache. Used by the git_checkout drop to place the clone
// and to emit its `path` output.
func GitCheckoutRel(graphID, nodeID string) string {
	return path.Join(GitCacheDirName, gitCacheSeg(graphID), gitCacheSeg(nodeID))
}

// GitCacheGraphRel is the workspace-relative directory holding all of one
// flow's checkout caches: gitcache/<flow>. The daemon removes this subtree
// when the flow is deleted so clones don't orphan.
func GitCacheGraphRel(graphID string) string {
	return path.Join(GitCacheDirName, gitCacheSeg(graphID))
}
