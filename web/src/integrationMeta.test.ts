// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from "vitest";
import { integrationMeta, integrationSlug } from "./integrationMeta";
import catalog from "./integrationMeta.catalog.json";

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
      expect(
        (entry.description ?? "").trim().length,
        `${slug}'s description is too thin to be useful`,
      ).toBeGreaterThan(80);
    }
  });
});
