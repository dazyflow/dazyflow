// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"os"
	"strings"
)

type SandboxProvider interface {
	// Implementations MUST reject identifiers unsafe to embed in a path, or a hostile
	// tenant/workspace name escapes the base directory.
	Root(tenant, workspace string) (string, error)
}

type ScratchProvider interface {
	SandboxProvider

	// Under the same (tenant, workspace) subtree as Root, so it counts against the
	// tenant's quota while alive. runID is validated like the other identifiers.
	ScratchRoot(tenant, workspace, runID string) (string, error)

	// Idempotent: a run that never created scratch returns nil.
	RemoveScratch(tenant, workspace, runID string) error
}

func IsSandboxEscape(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrInvalid) {
		return true
	}
	msg := err.Error()
	for _, marker := range []string{"path escapes", "outside root", "invalid argument"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
