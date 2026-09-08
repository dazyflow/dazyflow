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

export function formatDuration(startedISO: string, finishedISO: string): string {
  const start = Date.parse(startedISO);
  const end = Date.parse(finishedISO);
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "";
  const ms = Math.max(0, end - start);
  if (ms < 1000) return `${ms}${NBSP}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}${NBSP}s`;
  return `${(ms / 60_000).toFixed(1)}${NBSP}min`;
}

export function slugify(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}
