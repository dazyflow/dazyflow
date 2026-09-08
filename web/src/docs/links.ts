// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The docs chrome's addresses. The off-site ones live in lib/externalLinks —
// the app's auth pages point at the same product, source and licence, and two
// copies of a URL is one that gets left behind when the host moves.
//
// In-site destinations are NEVER written out as strings: they come off NAV
// (see DocsFooter), so a page that moves cannot leave a dead link behind.
export { SITE, SOURCE, LICENSE, CONTACT, INVITE } from "../lib/externalLinks";

export const DOCS_HOME = "/guide/concepts";
