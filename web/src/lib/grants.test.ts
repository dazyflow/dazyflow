// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { isGrantActive } from "./grants";
import type { AccessGrant, GrantStatus } from "../types";

// isGrantActive decides whether a support agent is currently allowed to read a
// customer's flows, so every branch here is a security boundary — and the
// function exists because two hand-rolled copies once disagreed about an
// unparseable expiry. These tests pin the fail-CLOSED tie-break so a future
// refactor can't quietly restore the fail-open reading.
describe("isGrantActive", () => {
  const now = new Date("2026-06-23T12:00:00Z");

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function grant(over: Partial<AccessGrant> = {}): AccessGrant {
    return {
      id: "g1",
      ticket_id: "t1",
      tenant: "acme",
      flow_id: "daily-invoice",
      agent_subject: "agent@vendor.com",
      status: "approved",
      requested_at: "2026-06-23T11:00:00Z",
      requested_by: "agent@vendor.com",
      expires_at: "2026-06-23T13:00:00Z", // an hour out from `now`
      ...over,
    };
  }

  it("is active for an approved, unexpired, unrevoked grant", () => {
    expect(isGrantActive(grant())).toBe(true);
  });

  // Only "approved" grants confer access. A requested one is still awaiting
  // the org's consent, and denied/expired/revoked have all been withdrawn.
  it("is inactive for every non-approved status", () => {
    const others: GrantStatus[] = ["requested", "denied", "revoked", "expired"];
    for (const status of others) {
      expect(isGrantActive(grant({ status }))).toBe(false);
    }
  });

  // A revoked_at stamp ends the grant immediately, even while the status still
  // reads approved and the expiry is in the future.
  it("is inactive once revoked, whatever the expiry says", () => {
    expect(
      isGrantActive(grant({ revoked_at: "2026-06-23T11:30:00Z" })),
    ).toBe(false);
  });

  it("is inactive at or past the expiry", () => {
    // Exactly at expiry is NOT active — the comparison is strictly greater.
    expect(isGrantActive(grant({ expires_at: now.toISOString() }))).toBe(false);
    expect(
      isGrantActive(grant({ expires_at: "2026-06-23T11:59:59Z" })),
    ).toBe(false);
  });

  it("is active a second before the expiry", () => {
    expect(
      isGrantActive(grant({ expires_at: "2026-06-23T12:00:01Z" })),
    ).toBe(true);
  });

  // The documented tie-break: an expiry that will not parse is NOT a licence
  // to keep showing the grant as live. The old SupportAgentHome copy read an
  // absent expiry as "never expires" and returned true; this must fail closed.
  it("fails closed on an expiry that cannot be parsed", () => {
    for (const expires_at of ["", "not-a-date", "never", "0000-13-45T99:99:99Z"]) {
      expect(isGrantActive(grant({ expires_at }))).toBe(false);
    }
  });

  // The type marks expires_at required, but the value crosses the wire as
  // JSON — a null or missing field must not become NaN-compares-true either.
  it("fails closed when the expiry is missing entirely", () => {
    expect(
      isGrantActive(grant({ expires_at: undefined as unknown as string })),
    ).toBe(false);
    expect(
      isGrantActive(grant({ expires_at: null as unknown as string })),
    ).toBe(false);
  });
});
