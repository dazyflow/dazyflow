// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Ordering for a table cell whose type nobody declared.
//
// Mirrors compareCells in drops/transform/sort_rows.go, and for the same
// reason: the values arrive as strings. A collection's store is all TEXT, and
// rows that came from a spreadsheet or a CSV are strings whatever they look
// like — so a plain string compare puts "10" before "9", which is the one
// ordering a reader will not accept from a column of numbers.
//
// The rules, in order:
//
//   blank (null / undefined / "")  first, in BOTH directions. A row with no
//                                  value hasn't got a place in the ordering;
//                                  parking it at one end keeps it from
//                                  drifting into the middle of the data, and
//                                  keeping that end fixed means flipping the
//                                  direction doesn't shuffle the blanks
//                                  through the rows you're reading. Same rule
//                                  the Sort rows step applies.
//   both numeric                   numeric compare, including string-encoded
//                                  numbers ("10" > "9").
//   both boolean                   false before true.
//   otherwise                      locale-aware string compare.
//
// The last rule is the one deliberate difference from the Go comparator, which
// compares bytes. This one sorts what a person is reading, so "Åsa" belongs
// after "Anna" rather than after "Z" — and `numeric: true` additionally orders
// "item2" before "item10", which byte order gets wrong.

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
