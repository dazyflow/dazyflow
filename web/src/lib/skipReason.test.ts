// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { cardSkipCopy, skipCopy } from "./skipReason";

describe("skip reason copy", () => {
  it("says nothing about a step that ran", () => {
    expect(cardSkipCopy("succeeded", undefined)).toBeNull();
    expect(cardSkipCopy("failed", undefined)).toBeNull();
    expect(cardSkipCopy(undefined, undefined)).toBeNull();
  });

  it("names the reason a trigger step went grey", () => {
    expect(cardSkipCopy("skipped", "trigger_not_fired")?.label).toBe("skip.notFired");
  });

  it("distinguishes a step the flow never reached", () => {
    expect(cardSkipCopy("skipped", "upstream")?.label).toBe("skip.upstream");
  });

  // Two chips saying the same thing is worse than one, and a disabled step
  // already carries its own "Off" chip.
  it("leaves a switched-off step to its own chip", () => {
    expect(cardSkipCopy("skipped", "step_off")).toBeNull();
  });

  // A code this build has no copy for must not silence the chip: the step
  // visibly did not run, and saying that much still beats saying nothing.
  it("falls back to a plain Skipped for an unknown or absent code", () => {
    expect(cardSkipCopy("skipped", "invented_later")?.label).toBe("skip.skipped");
    expect(cardSkipCopy("skipped", undefined)?.label).toBe("skip.skipped");
  });
});

// The run detail has no "Off" chip of its own, so it explains every skip —
// including the switched-off step the card stays quiet about.
describe("skip copy shared with the run detail", () => {
  it("explains a switched-off step", () => {
    expect(skipCopy("step_off").label).toBe("skip.stepOff");
  });

  it("explains the reasons the card shows too", () => {
    expect(skipCopy("trigger_not_fired").label).toBe("skip.notFired");
    expect(skipCopy("upstream").label).toBe("skip.upstream");
  });

  it("always returns copy, so a bare skip is never unexplained", () => {
    expect(skipCopy(undefined).label).toBe("skip.skipped");
    expect(skipCopy("invented_later").hint).toBe("skip.skippedHint");
  });
});
