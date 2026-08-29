// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// No HTML entities in the UI strings.
//
// They do not decode. A string rendered through <Trans> is parsed for its
// <0>…</0> component slots, not un-escaped, and one rendered through plain
// t() is inserted as text — so `&lt;` reaches the screen as the five
// characters `&lt;`, in every language at once.
//
// The trap is that escaping looks like the careful thing to do. The bearer-key
// help wanted to show `Authorization: Bearer <a key>`, and a literal `<a …>`
// inside a Trans string WOULD be eaten as a tag — so it was escaped, which
// swapped a disappearing placeholder for a visibly broken one. The fix was to
// stop needing the brackets: the sibling string a few keys away already wrote
// `Authorization: Bearer …`, and that is the convention.
//
// So if a string needs a literal angle bracket, it needs a different sentence
// — or the value interpolated in rather than written into the source, which
// is not parsed as markup either way.

import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

const I18N = __dirname;

// Named entities plus numeric ones. `&amp;` is included deliberately: a
// literal ampersand needs no escaping in either rendering path, so its
// presence means someone escaped for a markup layer that is not there.
const ENTITY = /&(?:[a-zA-Z][a-zA-Z0-9]{1,10}|#\d{1,5}|#[xX][0-9a-fA-F]{1,5});/;

function walk(value: unknown, path: string, hits: string[]): void {
  if (typeof value === "string") {
    const m = ENTITY.exec(value);
    if (m) hits.push(`${path}: ${m[0]}`);
    return;
  }
  if (value && typeof value === "object") {
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      walk(v, path ? `${path}.${k}` : k, hits);
    }
  }
}

describe("UI strings", () => {
  const files = readdirSync(I18N).filter((f) => f.endsWith(".json"));

  it("has translation files to check", () => {
    // A rename that emptied this list would make every assertion below pass
    // vacuously.
    expect(files.length).toBeGreaterThan(0);
  });

  for (const file of files) {
    it(`${file} contains no HTML entities`, () => {
      const json = JSON.parse(readFileSync(join(I18N, file), "utf8"));
      const hits: string[] = [];
      walk(json, "", hits);
      expect(
        hits,
        "HTML entities do not decode — they reach the screen literally. " +
          "Reword so the character is not needed (the bearer-key help uses " +
          "'Authorization: Bearer …' rather than angle brackets), or pass the " +
          "value in through interpolation, which is never parsed as markup.",
      ).toEqual([]);
    });
  }
});
