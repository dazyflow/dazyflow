// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"path"
	"strings"
)

const GitCacheDirName = "gitcache"

func gitCacheSeg(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return s
}

func GitCheckoutRel(graphID, nodeID string) string {
	return path.Join(GitCacheDirName, gitCacheSeg(graphID), gitCacheSeg(nodeID))
}

func GitCacheGraphRel(graphID string) string {
	return path.Join(GitCacheDirName, gitCacheSeg(graphID))
}
