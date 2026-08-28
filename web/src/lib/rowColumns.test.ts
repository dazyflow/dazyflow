// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { columnsOfRows } from "./rowColumns";

describe("columnsOfRows", () => {
  it("lists the columns in the order the producer emitted them", () => {
    expect(columnsOfRows([{ b: 1, a: 2 }])).toEqual(["b", "a"]);
  });

  it("counts a column that only appears in a later row", () => {
    // The reason this isn't Object.keys(rows[0]): ragged CSVs, APIs that omit
    // nulls, merged rowsets. The first row is a sample, not a schema.
    expect(columnsOfRows([{ a: 1 }, { a: 1, note: "late" }])).toEqual(["a", "note"]);
  });

  it("answers nothing for a value that isn't a list of rows", () => {
    // These all arrive off a run record, where the port could hold anything.
    for (const v of [undefined, null, "text", 42, { a: 1 }, [], [1, 2], [null]]) {
      expect(columnsOfRows(v)).toEqual([]);
    }
  });

  it("skips non-row entries without losing the rows around them", () => {
    expect(columnsOfRows([null, { a: 1 }, "x", { b: 2 }])).toEqual(["a", "b"]);
  });
});
