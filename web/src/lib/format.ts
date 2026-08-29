// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Value → display string helpers that more than one surface needs.
//
// Each of these existed two or three times over, copied between files. That is
// cheap right up until the copies drift, which formatDuration did: see its note.
// Timestamps live in ./datetime; this is for everything else.

// formatBytes renders a byte count as B / KiB / MiB / GiB / TiB. Binary units
// (1024), because it measures disk quota and file sizes, which is what the
// daemon reports. Was duplicated verbatim in Files, AdminWorkspace and
// PlanComparison.
// NBSP separates a number from its unit. A space is required there — SI says
// so for unit symbols, and Swedish writing rules say so too, which settles it
// for a UI that ships in both: "94ms" is not a house style we get to pick, it
// is wrong in one of our two languages.
//
// Non-breaking, and written as an escape rather than typed, so it is visible
// in the source and cannot be mistaken for an ordinary space. It keeps "94"
// and "ms" on the same line when a cell wraps — a value split across a line
// break reads as two things.
export const NBSP = "\u00A0";

export function formatBytes(n: number): string {
  if (n < 1024) return `${n}${NBSP}B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)}${NBSP}${units[i]}`;
}

// formatDuration renders the gap between two RFC3339 instants as a short
// human string: "840 ms", "4.6 s", "3.5 min".
//
// "min", not "m": m is the symbol for METRE. A three-and-a-half minute run
// used to render as "3.5m". Familiar from developer tooling ("3m29s"), and
// ambiguous the moment it is read as a unit rather than as jargon.
//
// This is the one that had actually drifted. The runs list and the run-detail
// page each carried their own copy, and they rounded differently — so ONE run
// read "1.25s" on its detail page and "1.3s" in the list it was linked from,
// and a 3m29s run read "3.5m" on one and "3m" on the other. Neither was wrong
// on its own terms; they simply disagreed about the same number.
//
// The surviving behaviour takes one decimal for seconds (the list's — two
// decimals is developer precision, not something a person reads off a table)
// and one decimal for minutes (the detail page's — rounding 3m29s to "3m"
// throws away the half-minute, and "3.5m" is no harder to read).
export function formatDuration(startedISO: string, finishedISO: string): string {
  const start = Date.parse(startedISO);
  const end = Date.parse(finishedISO);
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "";
  const ms = Math.max(0, end - start);
  if (ms < 1000) return `${ms}${NBSP}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}${NBSP}s`;
  return `${(ms / 60_000).toFixed(1)}${NBSP}min`;
}

// slugify turns a display name into an id-safe slug. Returns "" for input with
// nothing slug-worthy in it; callers that need a non-empty id supply their own
// fallback (CreateFlow uses "flow"), since the right default depends on what is
// being named.
export function slugify(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}
