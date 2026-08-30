// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { resendOutcome } from "./verifyResend";

describe("resendOutcome", () => {
  it("reports a real send", () => {
    expect(resendOutcome({ sent: true })).toBe("sent");
  });

  it("reports an account that was already verified", () => {
    // Nothing was emailed, but nothing needs to be — the banner should go
    // away rather than promise an inbox.
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
    // The mailer is unset or unreachable and the route answered 502. This
    // used to be swallowed by a bare catch, leaving no signal at all.
    expect(resendOutcome(null)).toBe("failed");
  });
});
