import { describe, it, expect } from "vitest";
import { looksPersonalTenant, tenantDisplayName } from "./orgDisplayName";
import type { WhoAmI } from "../types";

describe("looksPersonalTenant", () => {
  it("matches auto-minted usr_<hex> ids", () => {
    expect(looksPersonalTenant("usr_3c9fc4e3")).toBe(true);
    expect(looksPersonalTenant("usr_DEADBEEF")).toBe(true);
  });
  it("rejects org slugs and junk", () => {
    expect(looksPersonalTenant("acme")).toBe(false);
    expect(looksPersonalTenant("usr_")).toBe(false); // needs hex
    expect(looksPersonalTenant("usr_xyz")).toBe(false); // non-hex
    expect(looksPersonalTenant("")).toBe(false);
  });
});

describe("tenantDisplayName", () => {
  const me = (memberships: { tenant: string; display_name?: string }[]) =>
    ({ memberships } as unknown as WhoAmI);

  it("prefers an org membership's display_name", () => {
    expect(
      tenantDisplayName(me([{ tenant: "acme", display_name: "Acme Inc" }]), "acme", "Personal"),
    ).toBe("Acme Inc");
  });
  it("labels a personal usr_<hex> tenant with the personal fallback", () => {
    expect(tenantDisplayName(me([]), "usr_3c9fc4e3", "Personal")).toBe("Personal");
  });
  it("falls back to the raw id for an unknown non-personal tenant", () => {
    expect(tenantDisplayName(me([]), "other-org", "Personal")).toBe("other-org");
  });
  it("returns empty string for an empty tenant", () => {
    expect(tenantDisplayName(me([]), "", "Personal")).toBe("");
  });
});
