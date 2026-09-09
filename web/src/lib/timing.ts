// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// How often the UI asks the daemon anything, and how long transient feedback
// stays on screen.
//
// The tier is chosen by WHAT the poll is for, not by which file it lives in:
//
//   live        You are watching this thing change right now, and the poll is
//               gated on something being in flight — it stops the moment the
//               run finishes, so it can afford to be quick.
//   watched     A list you have open where anything could arrive at any time.
//               It cannot gate on a live status, because "something new
//               appeared" is the event, so it runs the whole time the surface
//               is open and pays for that by being slower.
//   background  A badge in the shell, for a surface you are not looking at.
//               Only has to be roughly right.
//
// If a surface needs its own number, that is a signal the tier boundaries are
// wrong, not that the surface is special.
export const POLL = {
  live: 2_000,
  watched: 5_000,
  background: 30_000,
} as const;

export const TICK = {
  second: 1_000,
  relative: 30_000,
} as const;

export const FEEDBACK = {
  // The "Copied" tick on a copy-to-clipboard button. One affordance, and it
  // used to revert after 1500ms in six places and 2000ms in three, depending
  // on the file. Long enough to register, short enough that the button is
  // ready again before you look back at it.
  copied: 1_500,
} as const;
