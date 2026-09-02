// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"strings"
)

// MaxGraphIDLen bounds a flow ID. It becomes a filename ("graphs/<id>.json"),
// so it has to fit one on every filesystem — 255 bytes is the common ceiling
// and the ".json" suffix plus a healthy margin comes off that.
const MaxGraphIDLen = 128

// ValidGraphID reports whether id is usable as a flow ID.
//
// A flow ID is not just a key: it names a file in the workspace repository and
// a component of the git tag that marks a revision published
// ("refs/tags/graphs/<id>/published"). Unvalidated, it was both a path and a
// ref-name injection — "a/../../escape" stored a flow OUTSIDE graphs/ that
// could then never be loaded, published or deleted, "." and ".." stored one
// under a nonsense name, an ID with a space saved but could never be published
// (a git ref may not contain one), and a 300-character ID worked on the
// in-memory store and failed with ENAMETOOLONG on disk.
//
// The accepted shape is deliberately narrower than either constraint: what the
// editor already produces (a lowercase slug), plus underscores and dots for
// hand-authored and API-authored IDs.
func ValidGraphID(id string) error {
	if id == "" {
		return fmt.Errorf("flow id is required")
	}
	if len(id) > MaxGraphIDLen {
		return fmt.Errorf("flow id is %d characters, limit is %d", len(id), MaxGraphIDLen)
	}
	if !isGraphIDStart(id[0]) {
		return fmt.Errorf("flow id %q must start with a letter or digit", id)
	}
	for i := 0; i < len(id); i++ {
		if !isGraphIDByte(id[i]) {
			return fmt.Errorf("flow id %q may only contain letters, digits, '-', '_' and '.'", id)
		}
	}
	// ".." would climb out of graphs/ as a path and is rejected outright as a
	// git ref name; a component ending in ".lock" is rejected as a ref too.
	if strings.Contains(id, "..") {
		return fmt.Errorf("flow id %q may not contain %q", id, "..")
	}
	if strings.HasSuffix(id, ".") || strings.HasSuffix(id, ".lock") {
		return fmt.Errorf("flow id %q may not end with %q or %q", id, ".", ".lock")
	}
	return nil
}

func isGraphIDStart(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func isGraphIDByte(b byte) bool {
	return isGraphIDStart(b) || b == '-' || b == '_' || b == '.'
}
