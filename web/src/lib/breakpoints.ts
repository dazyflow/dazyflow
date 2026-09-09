// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The viewport widths the layout changes at, in one place.
//
// Duplicated from the stylesheets unavoidably: custom properties are not usable
// inside a media query. Anything the CSS can do alone stays in CSS — these
// exist only where a component has to KNOW which layout is active, such as
// AppShell latching the sidebar across a breakpoint cross or FlowEditor
// switching the inspector between a side panel and a bottom sheet.
//
// scripts/check-css-breakpoints.mjs fails the build if a value here has no
// matching @media rule, so the mirror is checked rather than remembered.

export const MOBILE = 768;

// EDITOR_NARROW is where the flow editor stops reserving canvas room beside the
// inspector: below it the panel floats over the right edge instead, because
// narrowing a ~900px canvas by another 320 leaves less than one step's width.
// Mirrors `@media (min-width: 1101px)`.
//
// It is NOT where the inspector changes shape — that is MOBILE, and conflating
// the two put every window under 1100px in phone mode, where clicking a step
// selected it and nothing appeared.
export const EDITOR_NARROW = 1100;

export function isNarrower(breakpoint: number): boolean {
  if (typeof window === "undefined") return false;
  return window.innerWidth <= breakpoint;
}

// mediaQuery builds the matchMedia string for a breakpoint, so a call site
// can't drift from the `max-width` form the stylesheets use.
export function mediaQuery(breakpoint: number): string {
  return `(max-width: ${breakpoint}px)`;
}
