// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Rendering a table of untyped cells — for the screen, and for the CSV file
// the same table downloads as.
//
// Three surfaces show rows a flow produced: the Collections page, a run's
// Result panel, and the public collection link. They must agree, because the
// same person compares them: a timestamp that reads as local time on one page
// and as a UTC instant on another looks like two different values.

import { formatDateTime } from "./datetime";

// formatCell renders a cell for machines — the CSV column, the search index.
// Null shows blank; an object falls back to JSON so nothing renders
// "[object Object]".
export function formatCell(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

// An RFC3339 instant, which is what every timestamp a flow writes looks like
// (the Collections "Save rows" step stamps saved_at this way). Anchored and
// deliberately narrow: a free-text column that merely CONTAINS a date must
// not be rewritten, only a cell that is one.
const ISO_INSTANT =
  /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2})?(\.\d+)?(Z|[+-]\d{2}:?\d{2})$/;

// formatCellDisplay is formatCell for the screen. Timestamps come out of a
// store as "2026-08-31T07:21:54Z" — correct, and unreadable to the person the
// page is for, who also has to do the UTC arithmetic in their head. CSV keeps
// the raw value (see rowsToCSV): a spreadsheet wants the machine form.
export function formatCellDisplay(v: unknown): string {
  const s = formatCell(v);
  return ISO_INSTANT.test(s) ? formatDateTime(s) : s;
}

// rowsToCSV builds RFC-4180-ish CSV: fields are quoted and embedded quotes
// doubled. Good enough for the "open it in Excel/Sheets" path.
export function rowsToCSV(
  columns: string[],
  rows: Record<string, unknown>[],
): string {
  const esc = (s: string) => `"${s.replace(/"/g, '""')}"`;
  const header = columns.map(esc).join(",");
  const body = rows
    .map((r) => columns.map((c) => esc(formatCell(r[c]))).join(","))
    .join("\n");
  return body ? `${header}\n${body}` : header;
}
