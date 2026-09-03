// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import {
  cell,
  columnsOf,
  dataFaceView,
  facePorts,
  firstPortWithValue,
  MAX_CELL,
  MAX_COLUMNS,
  MAX_ROWS,
} from "./dataFace";
import type { Port } from "../types";

const port = (p: Partial<Port> & { port: string }): Port =>
  ({ mime: [], label: "", required: false, variadic: false, ...p }) as Port;

describe("cell", () => {
  it("collapses whitespace so a cell stays one line", () => {
    expect(cell("a\n  b\tc")).toBe("a b c");
  });

  it("renders an empty or absent value as a dash", () => {
    expect(cell("")).toBe("—");
    expect(cell(null)).toBe("—");
    expect(cell(undefined)).toBe("—");
  });

  it("collapses a nested object to JSON rather than [object Object]", () => {
    expect(cell({ a: 1 })).toBe('{"a":1}');
  });

  it("truncates past the cap", () => {
    expect(cell("x".repeat(MAX_CELL + 10))).toBe("x".repeat(MAX_CELL) + "…");
  });
});

describe("columnsOf", () => {
  it("unions keys across rows so an optional field is not hidden", () => {
    expect(columnsOf([{ a: 1 }, { a: 2, b: 3 }])).toEqual(["a", "b"]);
  });
});

describe("dataFaceView", () => {
  it("is empty with no ref at all", () => {
    expect(dataFaceView(undefined)).toEqual({ kind: "empty" });
  });

  it("reads a list of records as a table, capped by rows and columns", () => {
    const rows = Array.from({ length: 12 }, (_, i) => ({
      a: i, b: i, c: i, d: i, e: i,
    }));
    const view = dataFaceView({ data: rows });
    expect(view.kind).toBe("table");
    if (view.kind !== "table") return;
    expect(view.rows).toHaveLength(MAX_ROWS);
    expect(view.columns).toHaveLength(MAX_COLUMNS);
    expect(view.moreColumns).toBe(1);
    // total counts the whole list, not the sampled slice — it is the number
    // the footer reports.
    expect(view.total).toBe(12);
  });

  it("reads a list of non-records as text, not a column-less table", () => {
    expect(dataFaceView({ data: ["one", "two"] })).toMatchObject({
      kind: "text",
      text: "one\ntwo",
    });
  });

  it("reads a single object as a record", () => {
    const view = dataFaceView({ data: { category: "invoice", score: 0.94 } });
    expect(view).toMatchObject({
      kind: "record",
      fields: [
        { key: "category", value: "invoice", numeric: false },
        { key: "score", value: "0.94", numeric: true },
      ],
    });
  });

  it("reads a boolean as a yes/no", () => {
    expect(dataFaceView({ data: false })).toEqual({ kind: "bool", value: false });
  });

  it("names a by-reference output by its file name", () => {
    expect(dataFaceView({ ref: "blob://store/2026/Faktura.pdf", mime: "application/pdf" })).toEqual({
      kind: "file",
      name: "Faktura.pdf",
      mime: "application/pdf",
    });
  });

  it("treats an empty string, an empty list and a bare ref-less blank as empty", () => {
    expect(dataFaceView({ data: "   " })).toEqual({ kind: "empty" });
    expect(dataFaceView({ data: [] })).toEqual({ kind: "empty" });
    expect(dataFaceView({ mime: "application/json" })).toEqual({ kind: "empty" });
  });

  it("truncates long text and says so", () => {
    const view = dataFaceView({ data: "line\n".repeat(20) });
    expect(view).toMatchObject({ kind: "text", truncated: true });
  });
});

describe("facePorts", () => {
  it("drops the passthrough pin, which never holds what you opened the face for", () => {
    expect(facePorts([port({ port: "pass" }), port({ port: "out" })]).map((p) => p.port)).toEqual([
      "out",
    ]);
  });

  it("survives a drop with no declared outputs", () => {
    expect(facePorts(undefined)).toEqual([]);
  });
});

describe("firstPortWithValue", () => {
  const ports = [port({ port: "unmatched" }), port({ port: "matched" })];

  it("opens on the port that actually produced something", () => {
    expect(firstPortWithValue(ports, { matched: { data: [{ id: 1 }] } })).toBe("matched");
  });

  it("falls back to the first port when nothing has run", () => {
    expect(firstPortWithValue(ports, undefined)).toBe("unmatched");
  });

  it("is undefined when the drop declares no outputs", () => {
    expect(firstPortWithValue([], {})).toBeUndefined();
  });
});
