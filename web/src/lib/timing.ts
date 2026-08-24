// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// How often the UI asks the daemon anything, and how long transient feedback
// stays on screen.
//
// These were eleven bare literals with four named constants between them, and
// the values disagreed for no stated reason: the run-detail page polled a live
// run every 2s, the runs list polled the same runs every 3s, and the approvals
// inbox — which lists the very same `awaiting` runs — every 5s. Three surfaces
// showing one fact at three latencies. The support badge polled at 60s while
// the approvals badge beside it polled at 30s, both driving nothing but a
// number in the sidebar.
//
// The tier is chosen by WHAT the poll is for, not by which file it lives in:
//
//   live        You are watching this thing change right now, and the poll is
//               gated on something actually being in flight — it stops the
//               moment the run finishes. Costs nothing when idle, so it can
//               afford to be quick.
//   watched     A list you have open where anything could arrive at any time
//               (an inbox, a ticket thread, a wall display). It cannot gate on
//               a live status, because "something new appeared" is the event —
//               so it runs the whole time the surface is open, and pays for
//               that by being slower.
//   background  A badge in the shell, on every page, for a surface you are not
//               looking at. Only has to be roughly right.
//
// Being on a tier is the point: if a surface needs its own number, that is a
// signal the tier boundaries are wrong, not that the surface is special.
export const POLL = {
  live: 2_000,
  watched: 5_000,
  background: 30_000,
} as const;

// Render-only tickers. No network — these exist because a timestamp rendered
// once keeps claiming "just now" an hour later, so the component re-renders
// itself to keep a relative label or a clock honest.
export const TICK = {
  // A clock showing seconds has to tick every second to not look broken.
  second: 1_000,
  // Coarse "5m ago" labels; anything finer than a half-minute is wasted work.
  relative: 30_000,
} as const;

// How long transient confirmation stays visible before reverting.
export const FEEDBACK = {
  // The "Copied" tick on a copy-to-clipboard button. One affordance, and it
  // used to revert after 1500ms in six places and 2000ms in three, depending
  // on the file. Long enough to register, short enough that the button is
  // ready again before you look back at it.
  copied: 1_500,
} as const;
