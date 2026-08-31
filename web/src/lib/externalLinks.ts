// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Every address that leaves the product, in one place.
//
// These were accumulating a copy per call site — the docs URL had three, the
// invite mailto two — and the copies are the kind that rot quietly: a domain
// moves and the one nobody grepped for keeps pointing at the old host. Nothing
// here is a route; in-app destinations belong to the router, and the docs
// chrome derives its own from NAV.

// The product. Caddy serves the app on the apex domain and reverse-proxies
// docs.dazyflow.app to the docs container, so these are sibling hosts.
export const SITE = "https://dazyflow.app";
export const DOCS = "https://docs.dazyflow.app";

export const SOURCE = "https://github.com/dazyflow/dazyflow";

// Deep-linked to the licence file rather than to gnu.org: what governs the
// project is the copy in the tree, and a reader following this is usually
// checking the self-hosting terms against the version they have.
export const LICENSE = "https://github.com/dazyflow/dazyflow/blob/master/LICENSE";

export const CONTACT = "mailto:hi@dazyflow.app";

// The waitlist. A subject line so the reply lands in the right place without
// the sender having to explain themselves.
export const INVITE = "mailto:hi@dazyflow.app?subject=Dazyflow%20early%20access";

// The year on a footer's copyright line. A constant rather than
// `new Date().getFullYear()`: the build is what is copyrighted, and a footer
// that silently rolls over on 1 January claims a year the code has not been
// touched in.
export const COPYRIGHT_YEAR = 2026;
