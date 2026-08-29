// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { regionFlagEmoji, regionDisplayName, telFieldFlag } from "./phoneFlag";

describe("regionFlagEmoji", () => {
  it("maps an ISO region to its flag", () => {
    expect(regionFlagEmoji("SE")).toBe("🇸🇪");
    expect(regionFlagEmoji("GB")).toBe("🇬🇧");
    expect(regionFlagEmoji("US")).toBe("🇺🇸");
  });

  it("is case-insensitive", () => {
    expect(regionFlagEmoji("se")).toBe(regionFlagEmoji("SE"));
    expect(regionFlagEmoji("sE")).toBe(regionFlagEmoji("SE"));
  });

  // Anything that isn't exactly two letters yields "" rather than a pair of
  // stray regional-indicator codepoints.
  it("returns empty for a non-two-letter code", () => {
    for (const bad of ["", "S", "SWE", "S1", "12", "S-", " SE", "SE "]) {
      expect(regionFlagEmoji(bad)).toBe("");
    }
  });
});

describe("regionDisplayName", () => {
  it("resolves a region to a readable name", () => {
    // Intl is available in the test environment; assert the shape rather than a
    // locale-specific string so this doesn't depend on the runtime's CLDR data.
    const got = regionDisplayName("SE");
    expect(got.length).toBeGreaterThan(0);
    expect(got).not.toBe("");
  });

  it("upper-cases the code before resolving it", () => {
    expect(regionDisplayName("se")).toBe(regionDisplayName("SE"));
  });

  // Intl resolves an unassigned code to a placeholder rather than undefined, so
  // this documents that the ?? fallback is not the path an unknown code takes.
  it("returns Intl's placeholder for an unassigned code", () => {
    expect(regionDisplayName("ZZ").length).toBeGreaterThan(0);
  });

  // The catch is for a runtime with no Intl.DisplayNames at all (older
  // WebViews). Then the raw upper-cased code is the best available label — and
  // it must not throw, because this renders inside a form field.
  it("falls back to the raw code when Intl.DisplayNames is unavailable", () => {
    const real = Intl.DisplayNames;
    // @ts-expect-error — deliberately removing a platform API for this case.
    delete (Intl as unknown as Record<string, unknown>).DisplayNames;
    try {
      expect(regionDisplayName("se")).toBe("SE");
    } finally {
      (Intl as unknown as Record<string, unknown>).DisplayNames = real;
    }
  });

  it("falls back to the raw code when Intl.DisplayNames throws", () => {
    const real = Intl.DisplayNames;
    (Intl as unknown as Record<string, unknown>).DisplayNames = function () {
      throw new RangeError("unsupported");
    };
    try {
      expect(regionDisplayName("gb")).toBe("GB");
    } finally {
      (Intl as unknown as Record<string, unknown>).DisplayNames = real;
    }
  });
});

describe("telFieldFlag", () => {
  it("reads the region from a + international number", () => {
    expect(telFieldFlag("+46701234567")).toEqual({ flag: "🇸🇪", region: "SE" });
    expect(telFieldFlag("+4512345678")).toEqual({ flag: "🇩🇰", region: "DK" });
    expect(telFieldFlag("+442071234567")).toEqual({ flag: "🇬🇧", region: "GB" });
  });

  // "00" is the international dialing prefix across Europe: 0045… is +45.
  it("treats a 00 prefix as international", () => {
    expect(telFieldFlag("004512345678")).toEqual({ flag: "🇩🇰", region: "DK" });
    expect(telFieldFlag("0046701234567")).toEqual({ flag: "🇸🇪", region: "SE" });
  });

  // A SINGLE leading zero is a national trunk digit — 070… is a local Swedish
  // number, not country code 70. This is the distinction the comment calls out.
  it("treats a single leading zero as a local number", () => {
    expect(telFieldFlag("0701234567")).toBeNull();
    expect(telFieldFlag("08123456")).toBeNull();
  });

  it("returns null for a local number with no international form", () => {
    expect(telFieldFlag("0701234567")).toBeNull();
    expect(telFieldFlag("123456")).toBeNull();
    expect(telFieldFlag("(070) 123 45 67")).toBeNull();
  });

  it("returns null for an empty or whitespace field", () => {
    expect(telFieldFlag("")).toBeNull();
    expect(telFieldFlag("   ")).toBeNull();
  });

  // A wired reference is a template, not a number — there is no country to show.
  it("returns null for a wired reference", () => {
    expect(telFieldFlag("${node.phone}")).toBeNull();
    expect(telFieldFlag("  ${input.tel}  ")).toBeNull();
  });

  it("returns null for anything that isn't a string", () => {
    for (const bad of [null, undefined, 46, true, {}, [], NaN]) {
      expect(telFieldFlag(bad)).toBeNull();
    }
  });

  // Longest calling code wins: 354 is Iceland, and must not be read as "35"
  // (unmapped) or "3" (unmapped) and fall through to the globe.
  it("prefers the longest matching calling code", () => {
    expect(telFieldFlag("+3541234567")).toEqual({ flag: "🇮🇸", region: "IS" });
    expect(telFieldFlag("+3581234567")).toEqual({ flag: "🇫🇮", region: "FI" });
    // A one-digit code still resolves when no longer prefix matches.
    expect(telFieldFlag("+12125551234")).toEqual({ flag: "🇺🇸", region: "US" });
  });

  // Separators are a display convention, not data — they must not shift the
  // calling-code match.
  it("ignores spaces, dashes and parentheses", () => {
    const want = { flag: "🇸🇪", region: "SE" };
    expect(telFieldFlag("+46 70 123 45 67")).toEqual(want);
    expect(telFieldFlag("+46-70-123-45-67")).toEqual(want);
    expect(telFieldFlag("+46 (70) 123 4567")).toEqual(want);
    expect(telFieldFlag("  +46701234567  ")).toEqual(want);
  });

  // An unmapped code is still international, so it gets the globe rather than
  // null — the field IS in international form, we just can't name the country.
  it("falls back to a globe with no region for an unmapped code", () => {
    expect(telFieldFlag("+9991234567")).toEqual({ flag: "🌐", region: "" });
    expect(telFieldFlag("+8112345678")).toEqual({ flag: "🌐", region: "" });
  });

  // A bare "+" or "00" is international form with no digits at all: globe, not
  // a crash and not a wrong country.
  it("returns a globe for an international prefix with no digits", () => {
    expect(telFieldFlag("+")).toEqual({ flag: "🌐", region: "" });
    expect(telFieldFlag("00")).toEqual({ flag: "🌐", region: "" });
    expect(telFieldFlag("+ ")).toEqual({ flag: "🌐", region: "" });
  });

  // +1 is shared by the US and Canada. The module documents that ambiguous
  // codes resolve to ONE representative region — the backend's libphonenumber
  // is the source of truth — so this pins that it is deterministic, not that
  // it is correct for Canada.
  it("resolves an ambiguous code to one representative region", () => {
    expect(telFieldFlag("+16135551234")).toEqual({ flag: "🇺🇸", region: "US" });
  });
});
