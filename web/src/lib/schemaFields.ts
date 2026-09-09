// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Conditional field visibility for the params-schema forms.
//
// Some params only mean anything once another has a particular value: a
// free-text Custom format box beside a Format dropdown reads as two ways of
// saying the same thing, so people fill in the wrong one. x_advanced is the
// wrong tool for it — it would bury the field at the exact moment the user
// picks the option that needs it.
//
// Both form renderers go through here — the Inspector's SchemaForm and the
// inline editors on a node card — so a field cannot be conditional in one and
// permanent in the other.
import type { JSONSchema } from "../types";

// isFieldVisible reports whether a property should render, given its sibling
// params' current values and their schemas.
//
// A hidden field's stored value is deliberately left alone rather than cleared,
// so a value set by template or API survives a visit to the form.
//
// The sibling schemas are needed for their DEFAULTS: an unset param still has
// its default in force at run time, and a field gated on the default value of a
// dropdown would otherwise stay hidden until someone touched it. Strings
// compare case-insensitively, matching the drops' own leniency about enum
// casing, so the form and the runtime cannot disagree.
export function isFieldVisible(
  schema: JSONSchema,
  siblings: Record<string, unknown> | undefined,
  siblingSchemas: Record<string, JSONSchema> | undefined,
): boolean {
  const when = schema.x_visible_when;
  if (!when) return true;
  for (const [name, expected] of Object.entries(when)) {
    const actual = siblings?.[name] ?? siblingSchemas?.[name]?.default;
    const options = Array.isArray(expected) ? expected : [expected];
    if (!options.some((o) => sameValue(o, actual))) return false;
  }
  return true;
}

function sameValue(a: unknown, b: unknown): boolean {
  if (typeof a === "string" && typeof b === "string") {
    return a.toLowerCase() === b.toLowerCase();
  }
  return a === b;
}
