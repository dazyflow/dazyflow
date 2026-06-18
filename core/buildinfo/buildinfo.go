// Package buildinfo holds the version metadata stamped into the binary
// at build time. The three vars are set via the linker
// (-ldflags "-X git.sr.ht/~klahr/dazyflow/core/buildinfo.Version=...")
// by `make build` and the Dockerfile; see the Makefile's build/_bump
// targets and CHANGELOG.md for the release flow.
//
// They live in their own leaf package, not in main, so any package can
// read them without importing cmd/dzd — the daemon's HTTP gateway
// surfaces them on GET /api/v1, and the web UI shows the version in its
// account-menu footer. A plain `go build`/`go run` with no -ldflags
// (local dev) leaves the placeholder defaults below, so the values are
// always safe to print rather than empty strings.
package buildinfo

var (
	// Version is the release, from `git describe --tags --always
	// --dirty`. "dev" on an un-stamped build.
	Version = "dev"
	// Commit is the short git SHA the binary was built from.
	Commit = "unknown"
	// Date is the RFC3339 UTC build timestamp.
	Date = "unknown"
)

// String renders a one-line human summary for the startup banner, e.g.
// "v0.1.0 (commit a1b2c3d, built 2026-06-08T12:00:00Z)".
func String() string {
	return "v" + Version + " (commit " + Commit + ", built " + Date + ")"
}
