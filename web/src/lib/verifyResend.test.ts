// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { resendOutcome } from "./verifyResend";

describe("resendOutcome", () => {
  it("reports a real send", () => {
    expect(resendOutcome({ sent: true })).toBe("sent");
  });

  it("reports an account that was already verified", () => {
    expect(resendOutcome({ sent: false, already_verified: true })).toBe(
      "verified",
    );
  });

  it("treats a 200 that sent nothing as a failure, not a success", () => {
    // The regression this exists for: "the call didn't throw" was read as
    // "an email is on its way", so the banner promised an inbox that would
    // stay empty.
    expect(resendOutcome({ sent: false })).toBe("failed");
    expect(resendOutcome({})).toBe("failed");
  });

  it("treats a thrown request as a failure", () => {
    expect(resendOutcome(null)).toBe("failed");
  });
});
