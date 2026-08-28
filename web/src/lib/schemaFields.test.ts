// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { isFieldVisible } from "./schemaFields";
import type { JSONSchema } from "../types";

// The real shape this exists for: the Date & time step's Custom format field,
// gated on a Format dropdown whose default is a DIFFERENT value.
const siblingSchemas: Record<string, JSONSchema> = {
  format: { type: "string", default: "iso", enum: ["iso", "date", "custom"] } as JSONSchema,
  mode: { type: "string", default: "extract", enum: ["extract", "replace"] } as JSONSchema,
};
const gated = { type: "string", x_visible_when: { format: "custom" } } as JSONSchema;

describe("isFieldVisible", () => {
  it("shows a field with no condition", () => {
    expect(isFieldVisible({ type: "string" } as JSONSchema, {}, siblingSchemas)).toBe(true);
  });

  it("shows the field only when the sibling matches", () => {
    expect(isFieldVisible(gated, { format: "custom" }, siblingSchemas)).toBe(true);
    expect(isFieldVisible(gated, { format: "date" }, siblingSchemas)).toBe(false);
  });

  // An unset sibling still has its schema default in force at run time, so the
  // form must read it the same way: gated on "custom", default "iso" → hidden.
  it("falls back to the sibling's default when it is unset", () => {
    expect(isFieldVisible(gated, {}, siblingSchemas)).toBe(false);
    expect(isFieldVisible(gated, undefined, siblingSchemas)).toBe(false);
  });

  // The other half of that: a field gated on the value a dropdown ALREADY has
  // by default must be visible on a fresh node, not hidden until someone
  // touches the dropdown — which would look exactly like a broken form.
  it("shows a field gated on the sibling's own default", () => {
    const onDefault = { type: "string", x_visible_when: { mode: "extract" } } as JSONSchema;
    expect(isFieldVisible(onDefault, {}, siblingSchemas)).toBe(true);
    expect(isFieldVisible(onDefault, { mode: "replace" }, siblingSchemas)).toBe(false);
  });

  it("accepts a list of values", () => {
    const either = { type: "string", x_visible_when: { format: ["date", "custom"] } } as JSONSchema;
    expect(isFieldVisible(either, { format: "date" }, siblingSchemas)).toBe(true);
    expect(isFieldVisible(either, { format: "custom" }, siblingSchemas)).toBe(true);
    expect(isFieldVisible(either, { format: "iso" }, siblingSchemas)).toBe(false);
  });

  // The drops read enum values case-insensitively, so the form has to agree —
  // otherwise a param set to "Custom" by API or template runs as custom while
  // the form hides the field that configures it.
  it("compares strings case-insensitively", () => {
    expect(isFieldVisible(gated, { format: "Custom" }, siblingSchemas)).toBe(true);
  });

  it("requires every named sibling to match", () => {
    const both = {
      type: "string",
      x_visible_when: { format: "custom", mode: "replace" },
    } as JSONSchema;
    expect(isFieldVisible(both, { format: "custom", mode: "replace" }, siblingSchemas)).toBe(true);
    expect(isFieldVisible(both, { format: "custom", mode: "extract" }, siblingSchemas)).toBe(false);
  });

  // Missing sibling schemas is the API/preview path, not a crash.
  it("survives absent sibling schemas", () => {
    expect(isFieldVisible(gated, { format: "custom" }, undefined)).toBe(true);
    expect(isFieldVisible(gated, {}, undefined)).toBe(false);
  });
});
