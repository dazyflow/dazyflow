// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { primaryLanguage } from "./language";

describe("primaryLanguage", () => {
  it("drops the region", () => {
    // What i18next actually hands out on a Swedish machine, and what
    // core.Graph.Language wants to store.
    expect(primaryLanguage("sv-SE")).toBe("sv");
    expect(primaryLanguage("sv-FI")).toBe("sv");
    expect(primaryLanguage("en-GB")).toBe("en");
  });

  it("lower-cases", () => {
    expect(primaryLanguage("SV")).toBe("sv");
    expect(primaryLanguage("EN-US")).toBe("en");
  });

  it("passes a bare subtag through", () => {
    expect(primaryLanguage("sv")).toBe("sv");
  });

  it("answers empty for nothing, rather than throwing", () => {
    // Callers stamp the result onto a graph. An empty string is the value
    // core.Graph.Language already documents as "English", so a missing tag
    // degrades to the existing default instead of crashing the save.
    expect(primaryLanguage(undefined)).toBe("");
    expect(primaryLanguage(null)).toBe("");
    expect(primaryLanguage("")).toBe("");
  });
});
