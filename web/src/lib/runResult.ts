// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Edge, JobRecord, JobStatus, Ref } from "../types";
import { columnsOfRows } from "./rowColumns";

// What a person means by "the result" of a run is the output of the steps at
// the end of the flow — not the intermediate plumbing. Both the editor (after
// pressing Run) and the run-detail page answer that question, and they have
// to answer it the same way, so the picking and the formatting live here.

// RUN_PREVIEW_MAX caps an inline result so a step that emitted a thousand
// rows still leaves the banner a banner. Full values stay available in the
// run timeline's per-port disclosure.
export const RUN_PREVIEW_MAX = 600;

// previewOutput renders a step's output ports as one short human-readable
// blob: the first port carrying an inline value wins. Text passes through
// as-is (a rendered summary, a message body); anything structured is
// pretty-printed. Returns "" when the step produced nothing inline — which
// includes outputs held by reference (a large blob in storage), where the
// internal ref string would mean nothing to the reader.
export function previewOutput(
  output: Record<string, Ref> | undefined,
  max: number = RUN_PREVIEW_MAX,
): string {
  for (const port of Object.keys(output ?? {})) {
    const data = output?.[port]?.data;
    if (data == null) continue;
    let text: string;
    if (typeof data === "string") {
      text = data;
    } else {
      try {
        text = JSON.stringify(data, null, 2);
      } catch {
        text = String(data);
      }
    }
    if (text.trim() === "") continue;
    return text.length > max ? text.slice(0, max) + "…" : text;
  }
  return "";
}

// isResultNode reports whether a node sits at the end of the flow — no edge
// leaves it. Callers walk their own node list in execution order and take the
// first result node that produced something, so a fan-out with several
// endpoints still shows a real value rather than nothing.
export function isResultNode(nodeID: string, edges: Edge[] | undefined): boolean {
  return !(edges ?? []).some((e) => e.from === nodeID);
}

// ResultView is what the Result panel draws: the same value previewOutput
// summarizes, but classified so a rows value can become a real table instead
// of a wall of JSON braces. A run that ended in "Save rows" or "Group and
// count" produces exactly the thing a table is for, and a person reading
// `[\n  {\n    "region": "North"` is decoding a format, not reading an answer.
export type ResultView =
  | {
      kind: "rows";
      port: string;
      headers: string[];
      rows: Record<string, unknown>[];
    }
  | { kind: "text"; port: string; text: string; mime?: string }
  | { kind: "none" };

// isRowList reports whether a value is a rows list — a non-empty array whose
// entries are all plain objects. A mixed array (or an array of scalars) is
// not: rendering it as a table would invent columns for some entries and drop
// the rest, so it stays text.
function isRowList(v: unknown): v is Record<string, unknown>[] {
  return (
    Array.isArray(v) &&
    v.length > 0 &&
    v.every((r) => !!r && typeof r === "object" && !Array.isArray(r))
  );
}

// resultView classifies a step's output. The port choice is previewOutput's —
// first port carrying an inline value — so the panel and the banner never
// disagree about which port "the result" came from.
//
// Column order prefers the value's own headers, then appends any column the
// rows actually carry that they didn't declare: the producer's order is the
// right default, and a column missing from the header list is still data the
// reader is owed.
export function resultView(output: Record<string, Ref> | undefined): ResultView {
  for (const port of Object.keys(output ?? {})) {
    const ref = output?.[port];
    const data = ref?.data;
    if (data == null) continue;
    if (isRowList(data)) {
      const declared = (ref?.headers ?? []).filter((h) => h !== "");
      const seen = new Set(declared);
      const headers = [
        ...declared,
        ...columnsOfRows(data).filter((c) => !seen.has(c)),
      ];
      return { kind: "rows", port, headers, rows: data };
    }
    const text = typeof data === "string" ? data : safeJSON(data);
    if (text.trim() === "") continue;
    return { kind: "text", port, text, mime: ref?.mime };
  }
  return { kind: "none" };
}

// safeJSON pretty-prints a value, falling back to String for the cyclic /
// unserializable cases so the panel shows something rather than throwing
// inside a render.
function safeJSON(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

// RESULT_ROW_LIMIT caps the rows the panel renders. A step that emitted ten
// thousand rows should still leave the page scrollable and the timeline
// reachable; the CSV download carries every row it was given.
export const RESULT_ROW_LIMIT = 200;

// RESULT_TEXT_MAX is where the panel folds a long text result behind a
// "show everything" toggle. Far larger than RUN_PREVIEW_MAX (a one-line
// banner) because this is the surface a reader came to read.
export const RESULT_TEXT_MAX = 4000;

// resultFilename names the file a result downloads as. Extension follows the
// shape, not the MIME: rows always leave as CSV (the point is a spreadsheet),
// and text leaves as .json only when it really is JSON.
export function resultFilename(view: ResultView, flow: string): string {
  const stem = (flow || "result").replace(/[^\w.-]+/g, "-").replace(/^-|-$/g, "") || "result";
  if (view.kind === "rows") return `${stem}.csv`;
  if (view.kind === "text") {
    const json = view.mime?.includes("json") || /^\s*[[{]/.test(view.text);
    return `${stem}.${json ? "json" : "txt"}`;
  }
  return `${stem}.txt`;
}

// pickResultNode chooses whose output the Result panel shows, or null when the
// run has no value to lead with.
//
// Prefer a step at the end of the flow, so intermediate plumbing isn't
// mistaken for the answer; fall back to the last step that produced anything
// inline, which is the same node in a linear flow and the only option when the
// graph can't be loaded (a deleted flow).
//
// The exception is the reason this is a function rather than an expression: a
// flow whose end step emitted a FILE has no inline value at the end, and the
// fallback then reaches backwards past it and presents an upstream step's
// value as "the result". On a flow that reads a literal, converts it and
// writes a CSV, that means the panel showed the flow's own INPUT — directly
// above a Files panel holding the actual output. When the end of the flow
// produced a file, the file is the result, and the Files panel is what names
// it.
export function pickResultNode(
  nodes: JobRecord[],
  edges: Edge[] | undefined,
  runStatus: JobStatus,
): JobRecord | null {
  if (runStatus !== "succeeded") return null;
  const hasGraph = !!edges;
  const terminal = nodes.filter(
    (n) => n.Status === "succeeded" && (!hasGraph || isResultNode(n.NodeID, edges)),
  );
  const withValue = (n: JobRecord) => previewOutput(n.Result?.output) !== "";
  const endValue = [...terminal].reverse().find(withValue);
  if (endValue) return endValue;
  // No value at the end. Is that because the end wrote a file?
  const endWroteFile = terminal.some((n) =>
    Object.values(n.Result?.output ?? {}).some((r) => !!r?.ref),
  );
  if (endWroteFile) return null;
  return [...nodes].reverse().find((n) => n.Status === "succeeded" && withValue(n)) ?? null;
}
