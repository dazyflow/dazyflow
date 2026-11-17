// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// absoluteURL turns a server-relative path ("/invite/abc") into a link the
// user can paste somewhere else, using the browser's own origin. A URL that is
// already absolute is returned as-is, so callers need not know which form the
// server sent.
export function absoluteURL(path: string): string {
  if (/^https?:\/\//i.test(path)) return path;
  if (typeof window !== "undefined") return window.location.origin + path;
  return path;
}
