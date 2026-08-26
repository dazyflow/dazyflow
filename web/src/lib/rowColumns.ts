// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The column names of a rows value — what the editor shows when a step asks
// "which columns does my input have?".
//
// Two things this gets right that reading `Object.keys(rows[0])` does not:
//
//   A column missing from the first row still counts. Rows come from CSVs with
//   ragged lines, from APIs that omit null fields, and from merges — so the
//   first row is a sample, not a schema. Taking its keys as the answer hides
//   columns that are plainly there in row two.
//
//   A non-rows value answers "no columns" instead of throwing. The value comes
//   off a run record, so it can be a string, a number, or a single object; the
//   caller wants an empty list for those, not an exception in a panel.
//
// Order is first-seen, which is the order the producer emitted — the order the
// table should default to.
export function columnsOfRows(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const row of value) {
    if (!row || typeof row !== "object" || Array.isArray(row)) continue;
    for (const k of Object.keys(row as Record<string, unknown>)) {
      if (!seen.has(k)) {
        seen.add(k);
        out.push(k);
      }
    }
  }
  return out;
}
