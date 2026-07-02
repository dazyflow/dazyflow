import { describe, it, expect } from "vitest";
import { detectTrackingParams, stripTrackingParams } from "./trackingParams";

describe("detectTrackingParams", () => {
  it("finds UTM and click-id params", () => {
    expect(
      detectTrackingParams("https://x.com/a?utm_source=news&utm_medium=email&fbclid=abc"),
    ).toEqual(["utm_source", "utm_medium", "fbclid"]);
  });

  it("returns [] for a clean URL", () => {
    expect(detectTrackingParams("https://x.com/a?id=42&page=2")).toEqual([]);
  });

  it("ignores ambiguous functional keys (ref/src/source)", () => {
    // Deliberately NOT treated as trackers — too often functional.
    expect(detectTrackingParams("https://x.com/a?ref=nav&src=menu&source=x")).toEqual([]);
  });

  it("matches prefix families case-insensitively", () => {
    expect(detectTrackingParams("https://x.com/?MTM_campaign=q3&pk_kwd=shoes")).toEqual([
      "MTM_campaign",
      "pk_kwd",
    ]);
  });

  it("handles no query / no value / templated values without throwing", () => {
    expect(detectTrackingParams("https://x.com/path")).toEqual([]);
    expect(detectTrackingParams("")).toEqual([]);
    expect(detectTrackingParams("https://x.com/?utm_source=${trigger.x}")).toEqual([
      "utm_source",
    ]);
  });
});

describe("stripTrackingParams", () => {
  it("removes trackers but keeps functional params, base and fragment", () => {
    expect(
      stripTrackingParams("https://x.com/a?id=42&utm_source=news&page=2#sec"),
    ).toBe("https://x.com/a?id=42&page=2#sec");
  });

  it("drops the trailing ? when only trackers were present", () => {
    expect(stripTrackingParams("https://x.com/a?utm_source=news&fbclid=abc")).toBe(
      "https://x.com/a",
    );
  });

  it("leaves a clean URL untouched", () => {
    const u = "https://x.com/a?id=42";
    expect(stripTrackingParams(u)).toBe(u);
  });

  it("preserves a non-tracking value byte-for-byte (no re-encoding)", () => {
    expect(stripTrackingParams("https://x.com/?q=a+b%20c&utm_source=x")).toBe(
      "https://x.com/?q=a+b%20c",
    );
  });
});
