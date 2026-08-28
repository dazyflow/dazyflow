// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { anchorBelow } from "./anchorPop";

// A 1000x800 viewport for every case, so the numbers below read as "how far
// from which edge" rather than as arbitrary arithmetic.
const VP = { width: 1000, height: 800 };
const rect = (left: number, top: number, w = 24, h = 24) => ({
  left,
  top,
  right: left + w,
  bottom: top + h,
});

describe("anchorBelow", () => {
  it("aligns the panel's right edge with the trigger's, below it", () => {
    // The common case: a topbar three-dots at x=900, plenty of room.
    const pos = anchorBelow(rect(900, 16), { width: 220, height: 60 }, {
      viewport: VP,
    });
    expect(pos).toEqual({ left: 924 - 220, top: 40 + 6 });
  });

  it("pulls a panel back on screen instead of overflowing the left edge", () => {
    // The bug this exists for: right-aligning a 300px panel to a trigger 80px
    // from the left would put its left edge at -196 — half off-screen.
    const pos = anchorBelow(rect(80, 100), { width: 300, height: 60 }, {
      viewport: VP,
    });
    expect(pos.left).toBe(8);
  });

  it("pulls a panel back off the right edge too", () => {
    const pos = anchorBelow(rect(960, 100), { width: 300, height: 60 }, {
      align: "left",
      viewport: VP,
    });
    expect(pos.left).toBe(1000 - 8 - 300);
  });

  it("flips above the trigger when there is no room below", () => {
    // Trigger near the bottom, panel taller than the gap left under it.
    const pos = anchorBelow(rect(500, 700), { width: 200, height: 300 }, {
      viewport: VP,
    });
    expect(pos.top).toBe(700 - 6 - 300);
  });

  it("stays below, clamped, when neither side fits", () => {
    // A panel taller than the viewport: flipping above would clip the top,
    // which is worse — the opening words are the ones worth reading.
    const pos = anchorBelow(rect(500, 400), { width: 200, height: 900 }, {
      viewport: VP,
    });
    expect(pos.top).toBe(8);
  });

  it("keeps a panel wider than the viewport at the left margin", () => {
    const pos = anchorBelow(rect(500, 100), { width: 1200, height: 60 }, {
      viewport: VP,
    });
    expect(pos.left).toBe(8);
  });

  it("clamps for a trigger scrolled off the top of the viewport", () => {
    const pos = anchorBelow(rect(500, -200), { width: 200, height: 60 }, {
      viewport: VP,
    });
    expect(pos.top).toBe(8);
  });
});
