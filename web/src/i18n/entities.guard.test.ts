// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later


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
