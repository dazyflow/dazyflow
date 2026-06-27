// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { formatRelative } from "./datetime";

describe("formatRelative", () => {
  const now = new Date("2026-06-23T12:00:00Z");

  it("returns empty for missing/unparseable values", () => {
    expect(formatRelative(null, now)).toBe("");
    expect(formatRelative(undefined, now)).toBe("");
    expect(formatRelative("", now)).toBe("");
  });

  it("collapses the last 45s to 'just now'", () => {
    expect(formatRelative("2026-06-23T11:59:30Z", now)).toBe("just now");
  });

  it("renders minutes, hours, and days", () => {
    expect(formatRelative("2026-06-23T11:55:00Z", now)).toBe("5m ago");
    expect(formatRelative("2026-06-23T09:00:00Z", now)).toBe("3h ago");
    expect(formatRelative("2026-06-21T12:00:00Z", now)).toBe("2d ago");
  });

  it("hands off to an absolute date past a week", () => {
    // 30 days back — coarse 'ago' stops being useful, so we show the date.
    expect(formatRelative("2026-05-24T12:00:00Z", now)).toMatch(
      /^\d{4}-\d{2}-\d{2}$/,
    );
  });

  it("shows a full timestamp for future timestamps (clock skew)", () => {
    expect(formatRelative("2026-06-23T13:00:00Z", now)).toMatch(
      /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/,
    );
  });
});
