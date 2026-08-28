// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The viewport widths the layout changes at, in one place.
//
// These are duplicated from the stylesheets on purpose, and the duplication is
// unavoidable: CSS custom properties are not usable inside a media query, so a
// `@media (max-width: var(--mobile))` does not exist. Anything the CSS can do
// alone should stay in CSS — these exist only for behaviour a media query
// cannot express, where a component has to KNOW which layout is active:
// AppShell latching the sidebar open/closed across a breakpoint cross,
// DocsShell doing the same for its nav, and FlowEditor switching the inspector
// between a side panel and a bottom sheet.
//
// Before this module there were three mirror sites and two conventions:
// AppShell and DocsShell each declared their own `MOBILE_BREAK = 768`, and
// FlowEditor compared against a bare `1100` twice, with its obligation to match
// the stylesheet recorded only in a comment. scripts/check-css-breakpoints.mjs
// now fails the build if a value here has no matching @media rule, so the
// mirror is checked rather than remembered.

// MOBILE is the phone/tablet cutoff: below it the sidebar collapses to an
// overlay and the docs nav becomes a drawer. Mirrors `@media (max-width: 768px)`.
export const MOBILE = 768;

// EDITOR_NARROW is where the flow editor stops fitting a persistent inspector
// beside the canvas and moves it to a bottom sheet. Mirrors
// `@media (max-width: 1100px)`. Distinct from MOBILE because the editor needs
// far more horizontal room than a page of prose does.
export const EDITOR_NARROW = 1100;

// isNarrower reports whether the viewport is at or below a breakpoint. Returns
// false when there is no window (SSR, jsdom without layout) so callers render
// the wide layout rather than crashing.
export function isNarrower(breakpoint: number): boolean {
  if (typeof window === "undefined") return false;
  return window.innerWidth <= breakpoint;
}

// mediaQuery builds the matchMedia string for a breakpoint, so a call site
// can't drift from the `max-width` form the stylesheets use.
export function mediaQuery(breakpoint: number): string {
  return `(max-width: ${breakpoint}px)`;
}
