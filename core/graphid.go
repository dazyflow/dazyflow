// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"strings"
)

// Leaves room under the 255-byte filesystem ceiling for "graphs/<id>.json".
const MaxGraphIDLen = 128

// Guards both a path and a git ref name: a flow ID names a file in the workspace
// repo and a component of the published-revision tag. Unvalidated,
// "a/../../escape" stored a flow outside graphs/ that could never be loaded or
// deleted, and a 300-character id worked in memory and failed with ENAMETOOLONG.
//
// The accepted shape is narrower than either constraint: the editor's lowercase
// slug, plus underscores and dots for hand- and API-authored ids.
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
	// A component ending in ".lock" is rejected as a git ref too.
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
