// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from "vitest";
import { integrationMeta, integrationSlug } from "./integrationMeta";
import catalog from "./integrationMeta.catalog.json";

// Every app in the catalog needs prose on its /apps/:slug page. Without an
// entry here the page falls back to a title-cased slug and an EMPTY
// description — which is how nShift and Roaring shipped with blank pages: the
// drops were added, the curated copy was not, and nothing said so.
//
// integrationMeta.catalog.json is generated from the live drop registry
// (`make integration-catalog`), so adding a connector fails this test until
// its description is written.
describe("every integration has a description", () => {
  it("covers the whole catalog", () => {
    const missing = (catalog as string[]).filter((integration) => {
      const entry = integrationMeta[integrationSlug(integration)];
      return !entry || !entry.description?.trim();
    });
    expect(missing).toEqual([]);
  });

  it("names each one and says something useful", () => {
    for (const [slug, entry] of Object.entries(integrationMeta)) {
      expect(entry.name?.trim(), `${slug} has no name`).toBeTruthy();
      // Short enough to be a placeholder is the failure mode worth catching —
      // a one-liner like "Slack integration" helps nobody.
      expect(
        (entry.description ?? "").trim().length,
        `${slug}'s description is too thin to be useful`,
      ).toBeGreaterThan(80);
    }
  });
});
