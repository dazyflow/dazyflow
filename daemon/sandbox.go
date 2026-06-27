// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
)

// FSSandbox maps every (tenant, workspace) pair to a dedicated directory
// under BaseDir. Created lazily on first request.
//
// Filesystem layout:
//
//	<BaseDir>/<tenant>/<workspace>/
//
// Identifiers are validated to contain only [A-Za-z0-9_-] so a hostile
// tenant name can't smuggle in ".." or path separators. BaseDir itself is
// resolved to an absolute path at construction; FSSandbox then guarantees
// every returned root is under it.
type FSSandbox struct {
	base string

	mu    sync.Mutex
	roots map[string]string // "tenant/workspace" → resolved absolute path
}

// NewFSSandbox prepares BaseDir, creating it if missing.
func NewFSSandbox(base string) (*FSSandbox, error) {
	if base == "" {
		return nil, fmt.Errorf("FSSandbox: base directory required")
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, fmt.Errorf("create sandbox base %q: %w", base, err)
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox base: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox base symlinks: %w", err)
	}
	return &FSSandbox{base: resolved, roots: make(map[string]string)}, nil
}

func (s *FSSandbox) Root(tenant, workspace string) (string, error) {
	if !isSafeIdent(tenant) {
		return "", fmt.Errorf("unsafe tenant identifier %q", tenant)
	}
	if !isSafeIdent(workspace) {
		return "", fmt.Errorf("unsafe workspace identifier %q", workspace)
	}
	key := tenant + "/" + workspace
	s.mu.Lock()
	defer s.mu.Unlock()
	if path, ok := s.roots[key]; ok {
		return path, nil
	}
	path := filepath.Join(s.base, tenant, workspace)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("create sandbox %q: %w", path, err)
	}
	// Re-resolve symlinks after creation; if a symlink is planted at the
	// tenant level it'd break confinement, so we verify the resolved
	// path is still inside base.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox %q: %w", path, err)
	}
	if !strings.HasPrefix(resolved+string(filepath.Separator), s.base+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox %q escapes base %q", resolved, s.base)
	}
	s.roots[key] = resolved
	return resolved, nil
}

// scratchDirName is the per-workspace subdirectory under which each
// run's scratch lives: <base>/<tenant>/<workspace>/.scratch/<runID>/.
// Placing it under the workspace subtree means walkUsage (quota) counts
// scratch against the tenant's budget while it's alive, and frees it on
// reclaim.
const scratchDirName = ".scratch"

// ScratchRoot returns (creating if needed) the run's scratch directory.
// It sits beside the persistent workspace data, namespaced by run ID, so
// it's quota-counted yet trivially reclaimable as a unit.
func (s *FSSandbox) ScratchRoot(tenant, workspace, runID string) (string, error) {
	if !isSafeIdent(tenant) {
		return "", fmt.Errorf("unsafe tenant identifier %q", tenant)
	}
	if !isSafeIdent(workspace) {
		return "", fmt.Errorf("unsafe workspace identifier %q", workspace)
	}
	if !isSafeScratchID(runID) {
		return "", fmt.Errorf("unsafe run identifier %q", runID)
	}
	path := filepath.Join(s.base, tenant, workspace, scratchDirName, runID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("create scratch %q: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve scratch %q: %w", path, err)
	}
	if !strings.HasPrefix(resolved+string(filepath.Separator), s.base+string(filepath.Separator)) {
		return "", fmt.Errorf("scratch %q escapes base %q", resolved, s.base)
	}
	return resolved, nil
}

// RemoveScratch deletes a run's scratch directory. Idempotent:
// RemoveAll treats a missing path as success, so reclaiming a run that
// never wrote scratch is a no-op.
func (s *FSSandbox) RemoveScratch(tenant, workspace, runID string) error {
	if !isSafeIdent(tenant) || !isSafeIdent(workspace) || !isSafeScratchID(runID) {
		return fmt.Errorf("unsafe identifier in scratch reclaim (%q/%q/%q)", tenant, workspace, runID)
	}
	return os.RemoveAll(filepath.Join(s.base, tenant, workspace, scratchDirName, runID))
}

// isSafeScratchID validates a run scratch identifier. A loop body run
// namespaces its scratch per item with a "<parentRunID>/iN" sub-path so
// concurrent iterations don't collide on a shared scratch directory; accept
// "/"-separated segments as long as each segment is itself a safe identifier
// (which rejects "", ".", ".." and traversal). Reclaiming the parent run's
// scratch (RemoveScratch with the bare parent ID) still removes every nested
// item directory with it.
func isSafeScratchID(runID string) bool {
	if runID == "" {
		return false
	}
	for _, seg := range strings.Split(runID, "/") {
		if !isSafeIdent(seg) {
			return false
		}
	}
	return true
}

// RemoveTenant deletes a tenant's entire subtree (every workspace and all
// scratch beneath it) — the sandbox half of the GDPR erasure cascade
// (Art. 17). Idempotent. Drops any cached roots for the tenant so a later
// recreate re-resolves cleanly.
func (s *FSSandbox) RemoveTenant(tenant string) error {
	if !isSafeIdent(tenant) {
		return fmt.Errorf("unsafe tenant identifier %q", tenant)
	}
	s.mu.Lock()
	for key := range s.roots {
		if key == tenant || strings.HasPrefix(key, tenant+"/") {
			delete(s.roots, key)
		}
	}
	s.mu.Unlock()
	return os.RemoveAll(filepath.Join(s.base, tenant))
}

// isSafeIdent permits the same identifier shape as DNS labels plus
// underscores: tight enough to be safe in any path layer.
func isSafeIdent(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	// Reject "." and ".." which match the rune set above but are
	// dangerous.
	if s == "." || s == ".." {
		return false
	}
	// Avoid leading dots — common gotcha for hidden directories.
	if s[0] == '.' {
		return false
	}
	return true
}

// Ensure FSSandbox satisfies the interfaces at compile time.
var (
	_ core.SandboxProvider = (*FSSandbox)(nil)
	_ core.ScratchProvider = (*FSSandbox)(nil)
)
