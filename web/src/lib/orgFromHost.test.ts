// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { orgFromHost } from "./orgFromHost";

describe("orgFromHost", () => {
  it("extracts the org label from a subdomain", () => {
    expect(orgFromHost("acme.dazyflow.app", "dazyflow.app")).toBe("acme");
    expect(orgFromHost("my-org.dazyflow.app", "dazyflow.app")).toBe("my-org");
  });

  it("ignores a port on the host", () => {
    expect(orgFromHost("acme.dazyflow.app:8080", "dazyflow.app")).toBe("acme");
  });

  it("is case-insensitive", () => {
    expect(orgFromHost("ACME.DazyFlow.App", "dazyflow.app")).toBe("acme");
  });

  it("returns empty for the apex", () => {
    expect(orgFromHost("dazyflow.app", "dazyflow.app")).toBe("");
  });

  it("returns empty when no wildcard domain is configured", () => {
    expect(orgFromHost("acme.dazyflow.app", "")).toBe("");
  });

  it("returns empty for a host outside the wildcard domain", () => {
    expect(orgFromHost("acme.evil.com", "dazyflow.app")).toBe("");
    expect(orgFromHost("acme.notdazyflow.app", "dazyflow.app")).toBe("");
  });

  it("returns empty for multi-level subdomains", () => {
    expect(orgFromHost("a.b.dazyflow.app", "dazyflow.app")).toBe("");
  });

  it("returns empty for reserved labels", () => {
    expect(orgFromHost("www.dazyflow.app", "dazyflow.app")).toBe("");
    expect(orgFromHost("app.dazyflow.app", "dazyflow.app")).toBe("");
    expect(orgFromHost("api.dazyflow.app", "dazyflow.app")).toBe("");
  });

  it("returns empty for malformed labels", () => {
    expect(orgFromHost("-bad.dazyflow.app", "dazyflow.app")).toBe("");
    expect(orgFromHost("bad-.dazyflow.app", "dazyflow.app")).toBe("");
  });

  it("tolerates a leading dot on the configured domain", () => {
    expect(orgFromHost("acme.dazyflow.app", ".dazyflow.app")).toBe("acme");
  });
});
