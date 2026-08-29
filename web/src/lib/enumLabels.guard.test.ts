// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The UI must render a dropdown's LABEL, never its stored value.
//
// `enum` values are API vocabulary — "not_equals", "in_range", "fixed_line".
// `enumNames` is what a person reads. The Go side already insists every enum
// carries labels (drops/enum_labels_test.go); this is the other half of that
// bargain, and it went unheld for a long time: the mapping was open-coded in
// five places, and the one surface that had NOT open-coded it — a read-only
// enum on a node card — printed the identifier. A canvas node read
// "not_equals" while the Inspector beside it read "does not equal", each
// internally consistent, neither obviously broken.
//
// So the rule is now structural rather than remembered: `enumNames` is read
// in exactly one module, and every screen goes through its two helpers. A
// sixth open-coded copy is the thing this catches, because a sixth copy is
// how the fifth one came to be wrong.

import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { enumOptionLabel, enumValueLabel } from "./dropText";

const SRC = join(__dirname, "..");

// Where reading enumNames directly is legitimate:
//   dropText.ts — the helpers themselves, the one place that may.
//   types.ts    — the field's declaration on JSONSchema.
// Anything else should be calling a helper.
const ALLOWED = new Set(["lib/dropText.ts", "types.ts"]);

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === "dist") continue;
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      sourceFiles(full, out);
      continue;
    }
    if (!/\.(ts|tsx)$/.test(entry)) continue;
    // Tests may construct fixtures containing enumNames; they render through
    // the components under test, so they cannot bypass the helpers anyway.
    if (/\.test\.tsx?$/.test(entry)) continue;
    out.push(full);
  }
  return out;
}

describe("dropdown labels", () => {
  it("are read through the helpers, not open-coded", () => {
    const offenders: string[] = [];
    for (const file of sourceFiles(SRC)) {
      const rel = relative(SRC, file).split("\\").join("/");
      if (ALLOWED.has(rel)) continue;
      // Strip comments first: the rule is about code, not prose. Several
      // files legitimately EXPLAIN the enumNames convention, and a guard that
      // punished them for it would just teach people to stop writing it down.
      const src = readFileSync(file, "utf8")
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .replace(/(^|[^:])\/\/.*$/gm, "$1");
      if (/\benumNames\b/.test(src)) {
        offenders.push(rel);
      }
    }
    expect(
      offenders,
      "these read schema.enumNames directly — use enumValueLabel (for a stored " +
        "value) or enumOptionLabel (when walking schema.enum to build options) " +
        "from lib/dropText, so every screen labels a dropdown the same way",
    ).toEqual([]);
  });
});

describe("enumValueLabel", () => {
  const schema = {
    enum: ["equals", "not_equals", "in_range"],
    enumNames: ["equals", "does not equal", "is within range"],
  };

  it("maps a stored value to its label", () => {
    expect(enumValueLabel(schema, "not_equals")).toBe("does not equal");
  });

  it("returns a value the schema no longer lists as itself", () => {
    // A graph saved against an older version still has to render.
    expect(enumValueLabel(schema, "retired_op")).toBe("retired_op");
  });

  it("survives a schema with no labels at all", () => {
    // Legitimate for enums whose value IS the name a user knows — HTTP
    // methods, currency codes.
    expect(enumValueLabel({ enum: ["GET", "POST"] }, "POST")).toBe("POST");
  });

  it("is safe on an absent schema or value", () => {
    expect(enumValueLabel(undefined, "x")).toBe("x");
    expect(enumValueLabel(schema, undefined)).toBe("");
    expect(enumValueLabel(schema, null)).toBe("");
  });

  it("matches non-string values by their string form", () => {
    expect(
      enumValueLabel({ enum: [1, 2], enumNames: ["One", "Two"] }, 2),
    ).toBe("Two");
  });
});

describe("enumOptionLabel", () => {
  it("labels the i-th option", () => {
    expect(enumOptionLabel({ enum: [1, 2], enumNames: ["One", "Two"] }, 1)).toBe("Two");
  });

  it("falls back to the value when that option has no name", () => {
    expect(enumOptionLabel({ enum: [1, 2], enumNames: ["One"] }, 1)).toBe("2");
    expect(enumOptionLabel({ enum: ["GET", "POST"] }, 0)).toBe("GET");
  });
});
