// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Port, Ref } from "../types";

// Shaping a step's output for the card's data face — the panel that expands
// below the header — and for the dialog behind its "show all" button.
//
// The card is 200px wide and the run may have produced a thousand rows, so
// the card caps hard: it is the glance. The same shaping serves the dialog
// with the caps lifted (see DataFaceCaps), so a table reads as a table on
// both surfaces and only the truncation differs. Pure functions, so every cap
// is testable without a canvas.

export const MAX_ROWS = 3;
export const MAX_COLUMNS = 4;
export const MAX_CELL = 28;
export const MAX_FIELDS = 4;
export const MAX_TEXT_LINES = 5;
export const MAX_TEXT = 220;

// How much of a value a surface renders. The caps above are the card's, and
// they are deliberately brutal — three rows in 200px is a glance, not a read.
// The dialog is a reading surface with its own scroll, so it needs the same
// shaping (a table is still a table) with the truncation lifted.
export type DataFaceCaps = {
  rows: number;
  columns: number;
  cell: number;
  fields: number;
  textLines: number;
  text: number;
};

export const GLANCE_CAPS: DataFaceCaps = {
  rows: MAX_ROWS,
  columns: MAX_COLUMNS,
  cell: MAX_CELL,
  fields: MAX_FIELDS,
  textLines: MAX_TEXT_LINES,
  text: MAX_TEXT,
};

// Generous rather than infinite: a step can emit tens of thousands of rows,
// and the cost of rendering all of them is paid by the browser on the main
// thread. These bound the DOM, not the reading — the footer still reports the
// true total, so a clipped table says so.
export const FULL_CAPS: DataFaceCaps = {
  rows: 500,
  columns: 60,
  cell: 400,
  fields: 300,
  textLines: 4000,
  text: 200000,
};

export type DataFaceView =
  | { kind: "table"; columns: string[]; rows: string[][]; total: number; moreColumns: number }
  | { kind: "record"; fields: { key: string; value: string; numeric: boolean }[]; more: number }
  | { kind: "text"; text: string; truncated: boolean }
  | { kind: "file"; name: string; mime?: string }
  | { kind: "bool"; value: boolean }
  | { kind: "empty" };

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

// cell renders one value on a single line. Nested objects collapse to their
// JSON so a column of them still shows SOMETHING recognisable — a row whose
// every cell reads "[object Object]" is worse than no table at all.
export function cell(v: unknown, max = MAX_CELL): string {
  let s: string;
  if (v === null || v === undefined) s = "—";
  else if (typeof v === "string") s = v;
  else {
    try {
      s = JSON.stringify(v) ?? String(v);
    } catch {
      s = String(v);
    }
  }
  s = s.replace(/\s+/g, " ").trim();
  if (s === "") s = "—";
  return s.length > max ? s.slice(0, max) + "…" : s;
}

// columnsOf unions the keys of the sampled rows rather than reading only the
// first: a list whose first record happens to omit an optional field would
// otherwise hide that column for every row behind it.
export function columnsOf(rows: Record<string, unknown>[]): string[] {
  const seen: string[] = [];
  for (const row of rows) {
    for (const k of Object.keys(row)) {
      if (!seen.includes(k)) seen.push(k);
    }
  }
  return seen;
}

// fileNameOf reads the display name of an output held by reference — a blob in
// storage rather than an inline value. "Faktura.pdf" beats the ref URI.
function fileNameOf(ref: Ref): string | undefined {
  if (!ref.ref) return undefined;
  const base = ref.ref.replace(/^[a-z]+:\/\//, "").split("/").pop();
  return base || undefined;
}

// dataFaceView picks the rendering for one port's value. An absent, blank or
// empty value is "empty" rather than a rendering of nothing: the card then
// says what the port WILL carry, which is the whole point of a face you can
// open before a first run.
export function dataFaceView(
  ref: Ref | undefined,
  caps: DataFaceCaps = GLANCE_CAPS,
): DataFaceView {
  if (!ref) return { kind: "empty" };

  const v = ref.data;

  if (v === undefined || v === null) {
    const name = fileNameOf(ref);
    return name ? { kind: "file", name, mime: ref.mime } : { kind: "empty" };
  }

  if (typeof v === "boolean") return { kind: "bool", value: v };

  if (Array.isArray(v)) {
    const records = v.filter(isRecord);
    // A list of records is a table; a list of anything else (strings, numbers)
    // has no columns to name, so it reads better as lines of text.
    if (records.length === v.length && records.length > 0) {
      const sample = records.slice(0, caps.rows);
      const all = columnsOf(sample);
      const columns = all.slice(0, caps.columns);
      return {
        kind: "table",
        columns,
        rows: sample.map((r) => columns.map((c) => cell(r[c], caps.cell))),
        total: v.length,
        moreColumns: all.length - columns.length,
      };
    }
    if (v.length === 0) return { kind: "empty" };
    return textView(v.map((x) => cell(x, caps.text)).join("\n"), caps);
  }

  if (isRecord(v)) {
    const keys = Object.keys(v);
    return {
      kind: "record",
      fields: keys.slice(0, caps.fields).map((k) => ({
        key: k,
        value: cell(v[k], caps.cell),
        numeric: typeof v[k] === "number",
      })),
      more: Math.max(0, keys.length - caps.fields),
    };
  }

  if (typeof v === "string") return v.trim() === "" ? { kind: "empty" } : textView(v, caps);

  return textView(String(v), caps);
}

function textView(raw: string, caps: DataFaceCaps): DataFaceView {
  const lines = raw.split("\n");
  let text = lines.slice(0, caps.textLines).join("\n");
  let truncated = lines.length > caps.textLines;
  if (text.length > caps.text) {
    text = text.slice(0, caps.text);
    truncated = true;
  }
  return { kind: "text", text, truncated };
}

// Where a face's data came from. The distinction is the whole reason an
// example is safe to show: a preview built on a shipped example must never
// read as one built on a real run, or an author trusts a field that was
// never there.
export type DataFaceTier = "run" | "example" | "none";

// dataFaceSource picks what a port's face shows and says where it came from.
// A real value always wins; a port's shipped example fills in only when
// nothing has run.
export function dataFaceSource(
  ref: Ref | undefined,
  port: Port | undefined,
  caps: DataFaceCaps = GLANCE_CAPS,
): { tier: DataFaceTier; view: DataFaceView } {
  const live = dataFaceView(ref, caps);
  if (live.kind !== "empty") return { tier: "run", view: live };
  if (port?.example !== undefined && port.example !== null) {
    const shipped = dataFaceView({ data: port.example }, caps);
    if (shipped.kind !== "empty") return { tier: "example", view: shipped };
  }
  return { tier: "none", view: { kind: "empty" } };
}

// facePorts are the output ports worth a tab. The passthrough pin carries the
// input untouched, so it is never what someone opened the face to look at.
export function facePorts(outputs: Port[] | undefined): Port[] {
  return (outputs ?? []).filter((p) => p.port !== "pass");
}

// firstPortWithValue is the tab to open on: the port with the most real thing
// to show, so a router's empty branch does not greet you with an empty panel
// while its populated one sits behind a tab. Run data first, then a shipped
// example, then whatever is declared first.
export function firstPortWithValue(
  ports: Port[],
  outputs: Record<string, Ref> | undefined,
): string | undefined {
  if (!ports.length) return undefined;
  const byTier = (want: DataFaceTier) =>
    ports.find((p) => dataFaceSource(outputs?.[p.port], p).tier === want);
  return (byTier("run") ?? byTier("example") ?? ports[0]).port;
}
