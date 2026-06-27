// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { buildRowCEL, parseRowCEL, rowCondToCEL, type RowCond } from "./SchemaForm";

// The row-condition builder is the no-code face of the `filter` param
// on the row drops. It has to generate CEL the engine accepts and parse
// its own output back so reopening a saved flow shows the same form
// (and unparseable expressions fall back to the raw CEL box). These
// tests pin both halves so the builder and the engine can't drift.

describe("rowCondToCEL", () => {
  const cases: [RowCond, string][] = [
    [{ column: "status", op: "equals", value: "unpaid" }, 'row.status == "unpaid"'],
    [{ column: "status", op: "not_equals", value: "paid" }, 'row.status != "paid"'],
    [{ column: "name", op: "contains", value: "Ltd" }, 'string(row.name).contains("Ltd")'],
    [{ column: "amount", op: "gt", value: "100" }, "double(row.amount) > 100"],
    [{ column: "amount", op: "lt", value: "50" }, "double(row.amount) < 50"],
    [{ column: "notes", op: "is_empty", value: "" }, 'row.notes == ""'],
    [{ column: "notes", op: "is_not_empty", value: "" }, 'row.notes != ""'],
    [{ column: "due_date", op: "before_today", value: "" }, 'timestamp(string(row.due_date) + "T00:00:00Z") < now'],
    [{ column: "due_date", op: "after_today", value: "" }, 'timestamp(string(row.due_date) + "T00:00:00Z") > now'],
  ];
  it.each(cases)("%o -> %s", (cond, cel) => {
    expect(rowCondToCEL(cond)).toBe(cel);
  });

  it("escapes quotes in values", () => {
    expect(rowCondToCEL({ column: "title", op: "equals", value: 'a "quote"' })).toBe(
      'row.title == "a \\"quote\\""',
    );
  });
});

describe("buildRowCEL", () => {
  it("joins multiple conditions with &&", () => {
    expect(
      buildRowCEL([
        { column: "status", op: "equals", value: "unpaid" },
        { column: "amount", op: "gt", value: "0" },
      ]),
    ).toBe('row.status == "unpaid" && double(row.amount) > 0');
  });

  it("drops conditions with a blank column", () => {
    expect(buildRowCEL([{ column: "", op: "equals", value: "x" }])).toBe("");
  });
});

describe("parseRowCEL round-trips what the builder emits", () => {
  it.each([
    [[{ column: "status", op: "equals", value: "unpaid" }]],
    [[{ column: "name", op: "contains", value: "Ltd" }]],
    [[{ column: "amount", op: "gt", value: "100" }]],
    [[{ column: "due_date", op: "before_today", value: "" }]],
    [
      [
        { column: "status", op: "equals", value: "unpaid" },
        { column: "days_overdue", op: "gt", value: "0" },
      ],
    ],
  ] as [RowCond[]][])("round-trips %o", (conds) => {
    expect(parseRowCEL(buildRowCEL(conds))).toEqual(conds);
  });

  it("parses a hand-written simple expression", () => {
    expect(parseRowCEL('row.country == "SE"')).toEqual([
      { column: "country", op: "equals", value: "SE" },
    ]);
  });

  it("treats empty as no conditions", () => {
    expect(parseRowCEL("")).toEqual([]);
  });

  it("returns null for an expression the builder can't represent", () => {
    // A function the builder never emits -> fall back to the raw CEL box.
    expect(parseRowCEL("row.a.startsWith(row.b) || size(row.items) > 3")).toBeNull();
  });

  it("returns null when one clause of an AND is too complex", () => {
    expect(parseRowCEL('row.status == "x" && row.a in [1,2,3]')).toBeNull();
  });
});
