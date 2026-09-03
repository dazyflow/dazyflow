// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { formatCell, formatCellDisplay, rowsToCSV } from "./cells";

describe("formatCell", () => {
  it("renders blanks for absent values", () => {
    expect(formatCell(null)).toBe("");
    expect(formatCell(undefined)).toBe("");
  });

  it("stringifies scalars", () => {
    expect(formatCell(42)).toBe("42");
    expect(formatCell(false)).toBe("false");
    expect(formatCell("North")).toBe("North");
  });

  it("falls back to JSON rather than [object Object]", () => {
    expect(formatCell({ a: 1 })).toBe('{"a":1}');
  });
});

describe("formatCellDisplay", () => {
  it("renders an instant in local time", () => {
    // Whatever the runner's zone, the point is that it stopped being the
    // stored spelling — the reader is not doing UTC arithmetic.
    expect(formatCellDisplay("2026-08-31T07:21:54Z")).not.toBe(
      "2026-08-31T07:21:54Z",
    );
  });

  it("leaves a value that merely contains a date alone", () => {
    expect(formatCellDisplay("invoice 2026-08-31T07:21:54Z paid")).toBe(
      "invoice 2026-08-31T07:21:54Z paid",
    );
    expect(formatCellDisplay("2026-08-31")).toBe("2026-08-31");
  });

  it("passes ordinary text through", () => {
    expect(formatCellDisplay("North")).toBe("North");
  });
});

describe("rowsToCSV", () => {
  it("writes a header row and quotes every field", () => {
    expect(rowsToCSV(["region", "n"], [{ region: "North", n: 42 }])).toBe(
      '"region","n"\n"North","42"',
    );
  });

  it("doubles embedded quotes", () => {
    expect(rowsToCSV(["name"], [{ name: 'the "big" one' }])).toBe(
      '"name"\n"the ""big"" one"',
    );
  });

  it("keeps the machine spelling of a timestamp", () => {
    // The screen shows local time; a spreadsheet wants the instant.
    expect(rowsToCSV(["at"], [{ at: "2026-08-31T07:21:54Z" }])).toBe(
      '"at"\n"2026-08-31T07:21:54Z"',
    );
  });

  it("emits a header-only file for no rows", () => {
    expect(rowsToCSV(["region"], [])).toBe('"region"');
  });

  it("blanks a column a row does not carry", () => {
    expect(rowsToCSV(["a", "b"], [{ a: 1 }])).toBe('"a","b"\n"1",""');
  });
});
