// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// SandboxProvider maps a (tenant, workspace) pair to an absolute
// filesystem directory the workspace's modules are confined to. Production
// implementations live under daemon (FSSandbox); the interface is in core
// so engine and module code can depend on it without importing daemon.
type SandboxProvider interface {
	// Root returns the absolute path of the workspace's data root,
	// creating it if necessary. Implementations must reject identifiers
	// that aren't safe to embed in a filesystem path (path separators,
	// "..", etc.) so that a hostile tenant/workspace name can't escape
	// the base directory.
	Root(tenant, workspace string) (string, error)
}

// ScratchProvider is an optional extension of SandboxProvider for
// providers that support per-run ephemeral scratch directories — a place
// for intermediate artifacts that should not survive the run. A provider
// that implements it lets the engine populate Job.ScratchRoot and the
// dispatcher reclaim the directory when the run finishes. Providers that
// don't implement it simply leave ScratchRoot empty (scratch:// paths
// then fail with a clear error).
type ScratchProvider interface {
	SandboxProvider

	// ScratchRoot returns the absolute path of the run's scratch
	// directory, creating it if necessary. It lives under the same
	// (tenant, workspace) subtree as Root so it counts against the
	// tenant's disk quota while alive. runID is validated like the
	// tenant/workspace identifiers so it can't escape the base.
	ScratchRoot(tenant, workspace, runID string) (string, error)

	// RemoveScratch deletes the run's scratch directory and everything
	// under it. Idempotent — reclaiming a run that never created scratch
	// (or was already reclaimed) returns nil.
	RemoveScratch(tenant, workspace, runID string) error
}
