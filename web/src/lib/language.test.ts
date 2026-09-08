// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { primaryLanguage } from "./language";

describe("primaryLanguage", () => {
  it("drops the region", () => {
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
    expect(primaryLanguage(undefined)).toBe("");
    expect(primaryLanguage(null)).toBe("");
    expect(primaryLanguage("")).toBe("");
  });
});
