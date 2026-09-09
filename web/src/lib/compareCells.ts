// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Ordering for a table cell whose type nobody declared.
//
// Mirrors compareCells in drops/transform/sort_rows.go, and for the same
// reason: the values arrive as strings. A collection's store is all TEXT, so a
// plain string compare puts "10" before "9".
//
// Blanks sort first in BOTH directions — a row with no value has no place in
// the ordering, and pinning that end means flipping the direction doesn't
// shuffle blanks through the rows you're reading. Then numbers numerically,
// booleans false-first, everything else by locale-aware string compare.
//
// That last rule is the one deliberate difference from the Go comparator, which
// compares bytes: this one sorts what a person is reading, so "Åsa" belongs
// after "Anna" rather than after "Z", and `numeric: true` orders "item2" before
// "item10".

// asNumber returns the numeric value of v when it is a number or a string that
// is entirely a number. A string with trailing text ("12 kr") is NOT numeric:
// half-parsing it would order "12 kr" and "12" as equal.
function asNumber(v: unknown): number | undefined {
  if (typeof v === "number") return Number.isFinite(v) ? v : undefined;
  if (typeof v !== "string") return undefined;
  const s = v.trim();
  if (s === "") return undefined;
  const n = Number(s);
  return Number.isFinite(n) ? n : undefined;
}

function isBlank(v: unknown): boolean {
  return v === null || v === undefined || (typeof v === "string" && v.trim() === "");
}

function text(v: unknown): string {
  if (typeof v === "string") return v;
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

// compareCells returns <0, 0 or >0 for (a, b) in ascending order. `locale` is
// the active UI language; omitted, the browser's default collation is used.
export function compareCells(a: unknown, b: unknown, locale?: string): number {
  const ba = isBlank(a);
  const bb = isBlank(b);
  if (ba || bb) return ba && bb ? 0 : ba ? -1 : 1;

  const na = asNumber(a);
  const nb = asNumber(b);
  if (na !== undefined && nb !== undefined) return na < nb ? -1 : na > nb ? 1 : 0;

  if (typeof a === "boolean" && typeof b === "boolean") {
    return a === b ? 0 : a ? 1 : -1;
  }

  return text(a).localeCompare(text(b), locale, { numeric: true, sensitivity: "base" });
}

// sortRowsByColumn returns a new array ordered by one column. Blanks stay at
// the front in both directions (see compareCells), so `desc` reverses the
// values without dragging the empty rows through them.
//
// The sort is stable, which is what makes a second sort meaningful: sort by
// name, then by status, and rows sharing a status stay in name order.
export function sortRowsByColumn<T extends Record<string, unknown>>(
  rows: T[],
  column: string,
  desc: boolean,
  locale?: string,
): T[] {
  return rows.slice().sort((x, y) => {
    const a = x[column];
    const b = y[column];
    if (isBlank(a) || isBlank(b)) return compareCells(a, b, locale);
    const cmp = compareCells(a, b, locale);
    return desc ? -cmp : cmp;
  });
}
