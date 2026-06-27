// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"net/http"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
)

// InstallGuardedHTTPTransport routes go-git's https fetches through the given
// HTTP client — wired at boot to an SSRF-guarded client (blocks
// private/loopback/link-local at dial, e.g. cloud metadata), so a clone URL
// can't be turned into a request to internal services. go-git's InstallProtocol
// is process-global, so this affects every git_checkout / git_log clone.
//
// It's an explicit boot-time call (not an init) so tests that clone from a
// local http server aren't blocked by the guard.
func InstallGuardedHTTPTransport(c *http.Client) {
	gitclient.InstallProtocol("https", githttp.NewClient(c))
}
