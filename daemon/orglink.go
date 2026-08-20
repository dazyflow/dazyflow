// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/url"
	"strings"
)

// orglink.go builds the UI links the daemon mails out.
//
// The web app's routes carry no org segment — the active org is browser state
// (localStorage) plus the session's server-side scope. So a link to an
// org-scoped resource is ambiguous on its own: it opens against whichever org
// the recipient's browser last used, and for anyone who belongs to more than
// one that is usually the wrong one. The tenant-scoped loaders then answer
// "not found", which reads to the user as the run or ticket having vanished.
//
// withOrg pins the org onto such a link. The app honours the param on boot —
// re-scoping the session when needed and then landing on the deep-linked page
// (see web/src/lib/orgDeepLink.ts). An org the recipient can't act in is
// ignored client-side, so pinning it is never a way to reach something.
//
// Only pin an org on links to resources that are actually tenant-scoped. The
// support AGENT queue, for instance, resolves tickets cross-tenant by design
// (loadTicketForAgent), so pinning the filing org there would try to move the
// agent out of their own org for no reason.

// orgQueryParam is the query key the app reads to select an org. The sign-in
// page reads the same key for the unauthenticated case, and
// web/src/lib/orgDeepLink.ts exports it as ORG_PARAM — the three must agree, so
// don't rename one side alone.
const orgQueryParam = "org"

// withOrg appends ?org=<tenant> to an already-built absolute link, so the app
// opens it in the org the resource belongs to rather than whichever org the
// recipient last used.
//
// Returns rawURL untouched when either argument is empty: no link to pin (the
// deployment has no PublicBaseURL) or no org to pin it to (a single-tenant
// deployment, where the resource is unambiguous anyway). The tenant is escaped,
// so an id containing "&" or "=" can't graft extra params onto the link.
func withOrg(rawURL, tenant string) string {
	if rawURL == "" || tenant == "" {
		return rawURL
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + orgQueryParam + "=" + url.QueryEscape(tenant)
}
