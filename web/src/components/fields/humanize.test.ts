// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The label a param falls back to when its manifest carries no `title`.
//
// Around 200 of the catalog's params have no title, and this used to Title Case
// every word of the key while the ~340 titled ones render in sentence case. The
// result was visible in a single panel — "Column Types" sitting under "Unique
// by" — and in the generated step reference, which capitalises the other way.

import { describe, expect, it } from "vitest";
import { humanize } from "./SchemaForm";

describe("humanize", () => {
  it("capitalises the first word only", () => {
    // The case that surfaced it: the Collections step's untitled param.
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
