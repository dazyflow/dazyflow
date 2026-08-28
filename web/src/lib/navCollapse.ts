// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { MOBILE, isNarrower } from "./breakpoints";

// The sidebar-collapse preference, shared by the app shell and the docs shell.
//
// Both sidebars behave the same way: a phone or small tablet starts collapsed
// regardless of what you last chose, because there isn't room, while a desktop
// honours the saved choice. DocsShell's comment already said it "mirrors
// AppShell's rail behaviour" — it did, by carrying its own copy of both
// functions. They differed only in which localStorage key they closed over,
// which is the one thing that SHOULD differ: the two sidebars are independent
// preferences, so the key is now a parameter.

// savedCollapsePref reads the persisted desktop choice. Returns false when
// storage is unavailable (private mode, a strict-mode iframe), so the sidebar
// defaults to expanded rather than throwing.
export function savedCollapsePref(key: string): boolean {
  try {
    return localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

// initialNavCollapsed picks the first-paint state, synchronously, so the first
// frame already matches the viewport instead of flickering between the two
// widths. Viewport-driven collapse is deliberately NOT persisted — the stored
// value stays the desktop choice.
export function initialNavCollapsed(key: string): boolean {
  if (isNarrower(MOBILE)) return true;
  return savedCollapsePref(key);
}
