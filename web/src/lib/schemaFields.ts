// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Conditional field visibility for the params-schema forms.
//
// Some params only mean anything once another one has a particular value: the
// Date & time step's Custom format field is noise until Format is set to
// Custom, and worse than noise — a free-text format box beside a format
// dropdown reads as two ways of saying the same thing, so people fill in the
// wrong one.
//
// The mechanism already in the schema is x_advanced, and it is the wrong tool
// here: it would bury the field behind a disclosure at the exact moment the
// user picks the option that needs it. This shows the field when it applies
// and hides it otherwise.
//
// Both form renderers go through here — the Inspector's SchemaForm and the
// inline editors on a node card — so a field cannot be conditional in one and
// permanent in the other.
import type { JSONSchema } from "../types";

// isFieldVisible reports whether a property should render, given its sibling
// params' current values and their schemas.
//
// A hidden field's stored value is deliberately LEFT ALONE rather than
// cleared: flip Format to Date and back to Custom and the format you typed is
// still there. It also means a value set by template or API survives a visit
// to the form, the same way HIDDEN_FIELD_KEYS values do.
//
// The sibling schemas are needed for their DEFAULTS. An unset param still has
// its default in force at run time, so the form has to read it the same way —
// a field gated on the default value of a dropdown would otherwise stay hidden
// until someone touched that dropdown, which looks exactly like a broken form.
//
// Strings compare case-insensitively, matching the drops' own leniency about
// enum casing (the date step reads "custom" and "Custom" alike), so the form
// and the runtime can't disagree about whether a field applies.
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
