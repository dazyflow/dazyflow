// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { formatRelative } from "./datetime";

describe("formatRelative", () => {
  const now = new Date("2026-06-23T12:00:00Z");

  // Stand-in for i18next's t: resolves the relative.* keys to their English
  // templates and interpolates {{count}}, so these assertions exercise the
  // real key wiring without pulling in the i18n runtime. The catalogue splits
  // these into _one/_other; formatRelative passes the BASE key and lets
  // i18next pick, so the plural i18next would choose for each case is
  // inlined here.
  const en: Record<string, string> = {
    "relative.justNow": "just now",
    "relative.minutesAgo": "{{count}} minutes ago",
    "relative.hoursAgo": "{{count}} hours ago",
    "relative.daysAgo": "{{count}} days ago",
  };
  const t = (k: string, o?: Record<string, unknown>): string => {
    let s = en[k] ?? k;
    if (o) for (const [key, v] of Object.entries(o)) s = s.replace(`{{${key}}}`, String(v));
    return s;
  };

  it("returns empty for missing/unparseable values", () => {
    expect(formatRelative(null, t, now)).toBe("");
    expect(formatRelative(undefined, t, now)).toBe("");
    expect(formatRelative("", t, now)).toBe("");
  });

  it("collapses the last 45s to 'just now'", () => {
    expect(formatRelative("2026-06-23T11:59:30Z", t, now)).toBe("just now");
  });

  it("renders minutes, hours, and days", () => {
    expect(formatRelative("2026-06-23T11:55:00Z", t, now)).toBe("5 minutes ago");
    expect(formatRelative("2026-06-23T09:00:00Z", t, now)).toBe("3 hours ago");
    expect(formatRelative("2026-06-21T12:00:00Z", t, now)).toBe("2 days ago");
  });

  it("hands off to an absolute date past a week", () => {
    // 30 days back — coarse 'ago' stops being useful, so we show the date.
    expect(formatRelative("2026-05-24T12:00:00Z", t, now)).toMatch(
      /^\d{4}-\d{2}-\d{2}$/,
    );
  });

  it("shows a full timestamp for future timestamps (clock skew)", () => {
    expect(formatRelative("2026-06-23T13:00:00Z", t, now)).toMatch(
      /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/,
    );
  });
});
