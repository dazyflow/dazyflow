// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// absoluteURL turns a server-relative path ("/invite/abc") into a link that
// works when pasted somewhere else, using the origin this page was served
// from. A URL that is already absolute is returned unchanged.
export function absoluteURL(path: string): string {
  if (/^https?:\/\//i.test(path)) return path;
  if (typeof window !== "undefined") return window.location.origin + path;
  return path;
}
