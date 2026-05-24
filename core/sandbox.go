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
