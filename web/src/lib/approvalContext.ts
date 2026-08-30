// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// approvalContext shapes the value a flow wired into an approval step into
// something an approver can read at a glance.
//
// The inbox used to show only which step was waiting, so someone was asked to
// approve a stranger's words into a testimonial list, or a refund, without
// being shown either. The value was already carried on the step — it just
// never reached the card.
//
// The value is whatever the flow wired in, so this normalizes the shapes that
// actually turn up: a form submission or a looked-up record (an object → named
// fields), a drafted reply or a summary (a string → text), and everything else
// (numbers, lists, nested objects → compact text), rather than rendering raw
// JSON at a non-technical reader.

export type ApprovalContextView =
  | { kind: "text"; text: string }
  | { kind: "fields"; fields: { key: string; value: string }[]; more: number }
  | null;

// MAX_FIELDS keeps one wide record from burying the Approve button. The run
// page has the whole thing; this is the gist.
const MAX_FIELDS = 8;
// MAX_VALUE trims a single long answer (a free-text form field can run to
// paragraphs) so one field can't crowd out the rest.
const MAX_VALUE = 300;

function clip(s: string): string {
  const t = s.trim();
  return t.length > MAX_VALUE ? t.slice(0, MAX_VALUE).trimEnd() + "…" : t;
}

// display renders one field's value. Primitives read as themselves; a nested
// object or list becomes compact JSON, which is honest about being structured
// without pretending to be prose.
function display(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "string") return clip(v);
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  try {
    return clip(JSON.stringify(v));
  } catch {
    return "";
  }
}

// order, when the value carries one, is the sequence its producer declared —
// a hosted form's field order, say. JSON objects have no order a reader can
// rely on (Go serializes map keys sorted), so without it a submission renders
// alphabetically and the person's name lands under their long answer.
export function approvalContextView(
  value: unknown,
  order?: string[],
): ApprovalContextView {
  if (value === null || value === undefined) return null;

  if (typeof value === "string") {
    const t = value.trim();
    return t ? { kind: "text", text: clip(t) } : null;
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return { kind: "text", text: String(value) };
  }

  // A single-row list is how a form submission reaches a row-writing step, and
  // it reads far better unwrapped than as a one-item array.
  if (Array.isArray(value)) {
    if (value.length === 1) return approvalContextView(value[0], order);
    if (value.length === 0) return null;
    return { kind: "text", text: display(value) };
  }

  if (typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>).filter(
      // A field the person left blank tells the approver nothing, and an empty
      // row is worse than an absent one.
      ([, v]) => display(v) !== "",
    );
    if (entries.length === 0) return null;
    // Declared order first, then anything the producer didn't name (extra
    // fields a caller posted) in the order we received them.
    if (order && order.length > 0) {
      const rank = new Map(order.map((k, i) => [k, i]));
      entries.sort(
        ([a], [b]) =>
          (rank.get(a) ?? Number.MAX_SAFE_INTEGER) -
          (rank.get(b) ?? Number.MAX_SAFE_INTEGER),
      );
    }
    const shown = entries.slice(0, MAX_FIELDS);
    return {
      kind: "fields",
      fields: shown.map(([key, v]) => ({ key, value: display(v) })),
      more: entries.length - shown.length,
    };
  }
  return null;
}
