// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The off-site addresses the docs chrome points at, in one place because the
// header and the footer point at the same ones. INVITE in particular was
// already duplicated the moment a second component wanted it.
//
// Everything here leaves the docs. In-site destinations are NEVER written out
// as strings — they come off NAV (see DocsFooter), so a page that moves cannot
// leave a dead link behind.

// The product itself. Caddy serves the app on the apex domain and
// reverse-proxies docs.dazyflow.app to the docs container, so this is a
// sibling host rather than a path on this one.
export const SITE = "https://dazyflow.app";

export const SOURCE = "https://git.sr.ht/~klahr/dazyflow";

// Deep-linked to the licence file rather than to gnu.org: what governs the
// project is the copy in the tree, and a reader following this is usually
// checking the self-hosting terms against the version they have.
export const LICENSE = "https://git.sr.ht/~klahr/dazyflow/tree/master/item/LICENSE";

export const CONTACT = "mailto:hi@dazyflow.app";

// The waitlist. A subject line so the reply lands in the right place without
// the sender having to explain themselves.
export const INVITE = "mailto:hi@dazyflow.app?subject=Dazyflow%20early%20access";

// Where the docs' own brand mark leads. NOT "/" — nginx serves index.html for
// any unmatched path, so "/" boots the SPA at a route the page map has no
// entry for and the reader lands on "Page not found". The first guide page is
// the docs' actual front door.
export const DOCS_HOME = "/guide/concepts";
