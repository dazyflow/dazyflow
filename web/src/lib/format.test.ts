// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { NBSP, formatBytes, formatDuration, slugify } from "./format";

// formatDuration is here because it had two implementations that disagreed: the
// runs list rounded seconds to one decimal and minutes to a whole number, the
// run-detail page did the opposite. One run therefore read "1.25s" on the page
// you clicked through to and "1.3s" in the list you clicked from. These pin the
// surviving rounding so a future copy can't quietly reintroduce a second one.
describe("formatDuration", () => {
  const d = (ms: number) =>
    formatDuration("2026-08-24T09:00:00.000Z", new Date(Date.parse("2026-08-24T09:00:00.000Z") + ms).toISOString());

  it("renders sub-second gaps in whole milliseconds", () => {
    expect(d(0)).toBe(`0${NBSP}ms`);
    expect(d(840)).toBe(`840${NBSP}ms`);
    expect(d(999)).toBe(`999${NBSP}ms`);
  });

  it("renders seconds to one decimal, not two", () => {
    expect(d(1000)).toBe(`1.0${NBSP}s`);
    expect(d(1250)).toBe(`1.3${NBSP}s`);
    expect(d(4567)).toBe(`4.6${NBSP}s`);
    expect(d(59_949)).toBe(`59.9${NBSP}s`);
  });

  it("renders minutes to one decimal, so a half-minute survives", () => {
    expect(d(60_000)).toBe(`1.0${NBSP}min`);
    expect(d(90_000)).toBe(`1.5${NBSP}min`);
    // 3m29s. The runs list used to round this to "3m" and drop the half.
    expect(d(209_000)).toBe(`3.5${NBSP}min`);
  });

  it("clamps a finish that precedes the start rather than going negative", () => {
    expect(
      formatDuration("2026-08-24T09:00:05.000Z", "2026-08-24T09:00:00.000Z"),
    ).toBe(`0${NBSP}ms`);
  });

  it("separates the number from the unit with a NON-BREAKING space", () => {
    // The space is required (SI, and Swedish writing rules); non-breaking so a
    // wrapping cell cannot leave "94" on one line and "ms" on the next. An
    // ordinary space would pass a naive eyeball test and fail this one.
    expect(d(840)).toContain(NBSP);
    expect(d(840)).not.toContain(" ");
  });

  it("uses min for minutes, because m is metre", () => {
    expect(d(90_000)).toMatch(/min$/);
  });

  it("returns empty for an unparseable instant, so a cell shows nothing", () => {
    expect(formatDuration("", "2026-08-24T09:00:00.000Z")).toBe("");
    expect(formatDuration("2026-08-24T09:00:00.000Z", "not a date")).toBe("");
  });
});

describe("formatBytes", () => {
  it("uses binary units, matching what the daemon reports", () => {
    expect(formatBytes(0)).toBe(`0${NBSP}B`);
    expect(formatBytes(1023)).toBe(`1023${NBSP}B`);
    expect(formatBytes(1024)).toBe(`1.0${NBSP}KiB`);
    expect(formatBytes(1536)).toBe(`1.5${NBSP}KiB`);
    expect(formatBytes(1024 ** 2)).toBe(`1.0${NBSP}MiB`);
    expect(formatBytes(1024 ** 3)).toBe(`1.0${NBSP}GiB`);
  });

  it("stops at TiB rather than inventing a unit", () => {
    expect(formatBytes(1024 ** 4)).toBe(`1.0${NBSP}TiB`);
    expect(formatBytes(1024 ** 5)).toBe(`1024.0${NBSP}TiB`);
  });
});

describe("slugify", () => {
  it("lowercases and collapses runs of non-alphanumerics to one hyphen", () => {
    expect(slugify("Order received alert")).toBe("order-received-alert");
    expect(slugify("Fortnox → Slack!!")).toBe("fortnox-slack");
  });

  it("trims edge hyphens", () => {
    expect(slugify("  --Hello--  ")).toBe("hello");
  });

  // Deliberately not "flow": the caller picks the fallback, because the right
  // default depends on what is being named. CreateFlow wraps this as flowSlug.
  it("returns empty when nothing slug-worthy is left", () => {
    expect(slugify("!!!")).toBe("");
    expect(slugify("")).toBe("");
  });
});
