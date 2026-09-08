// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later


import { describe, expect, it } from "vitest";
import { humanize } from "./SchemaForm";

describe("humanize", () => {
  it("capitalises the first word only", () => {
    expect(humanize("column_types")).toBe("Column types");
    expect(humanize("first_row_headers")).toBe("First row headers");
    expect(humanize("timeout_ms")).toBe("Timeout ms");
  });

  it("treats hyphens as separators too", () => {
    expect(humanize("reply-to")).toBe("Reply to");
  });

  it("leaves a single word alone but for its first letter", () => {
    expect(humanize("table")).toBe("Table");
    expect(humanize("URL")).toBe("URL");
  });

  it("survives an empty or separator-only key", () => {
    expect(humanize("")).toBe("");
    expect(humanize("__")).toBe("");
  });
});
