// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The docs chrome's addresses. The off-site ones live in lib/externalLinks —
// the app's auth pages point at the same product, source and licence, and two
// copies of a URL is one that gets left behind when the host moves.
//
// In-site destinations are NEVER written out as strings: they come off NAV
// (see DocsFooter), so a page that moves cannot leave a dead link behind.
export { SITE, SOURCE, LICENSE, CONTACT, INVITE } from "../lib/externalLinks";

// Where the docs' own brand mark leads. NOT "/" — nginx serves index.html for
// any unmatched path, so "/" boots the SPA at a route the page map has no
// entry for and the reader lands on "Page not found". The first guide page is
// the docs' actual front door.
export const DOCS_HOME = "/guide/concepts";
