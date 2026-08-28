// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { shouldShowTenantID } from "./visibleTenant";
import type { WhoAmI } from "../types";

function whoami(
  patch: Partial<WhoAmI> = {},
): WhoAmI {
  return {
    subject: "u@x.test",
    tenant: "usr_abc",
    workspace: "main",
    roles: [],
    permissions: [],
    ...patch,
  };
}

describe("shouldShowTenantID", () => {
  it("is false when no principal is loaded yet", () => {
    expect(shouldShowTenantID(null, 1)).toBe(false);
  });

  it("is false for a regular user in a single tenant", () => {
    expect(shouldShowTenantID(whoami(), 1)).toBe(false);
  });

  it("is true for a platform admin even in one tenant", () => {
    expect(
      shouldShowTenantID(whoami({ permissions: ["platform:admin"] }), 1),
    ).toBe(true);
  });

  it("is true when the principal can reach multiple tenants", () => {
    expect(shouldShowTenantID(whoami(), 2)).toBe(true);
  });

  it("is false when tenants is 0 (uninitialised list)", () => {
    expect(shouldShowTenantID(whoami(), 0)).toBe(false);
  });
});
