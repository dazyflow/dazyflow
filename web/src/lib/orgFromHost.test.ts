import { describe, expect, it } from "vitest";
import { orgFromHost } from "./orgFromHost";

describe("orgFromHost", () => {
  it("extracts the org label from a subdomain", () => {
    expect(orgFromHost("acme.hazyflow.app", "hazyflow.app")).toBe("acme");
    expect(orgFromHost("my-org.hazyflow.app", "hazyflow.app")).toBe("my-org");
  });

  it("ignores a port on the host", () => {
    expect(orgFromHost("acme.hazyflow.app:8080", "hazyflow.app")).toBe("acme");
  });

  it("is case-insensitive", () => {
    expect(orgFromHost("ACME.HazyFlow.App", "hazyflow.app")).toBe("acme");
  });

  it("returns empty for the apex", () => {
    expect(orgFromHost("hazyflow.app", "hazyflow.app")).toBe("");
  });

  it("returns empty when no wildcard domain is configured", () => {
    expect(orgFromHost("acme.hazyflow.app", "")).toBe("");
  });

  it("returns empty for a host outside the wildcard domain", () => {
    expect(orgFromHost("acme.evil.com", "hazyflow.app")).toBe("");
    expect(orgFromHost("acme.nothazyflow.app", "hazyflow.app")).toBe("");
  });

  it("returns empty for multi-level subdomains", () => {
    expect(orgFromHost("a.b.hazyflow.app", "hazyflow.app")).toBe("");
  });

  it("returns empty for reserved labels", () => {
    expect(orgFromHost("www.hazyflow.app", "hazyflow.app")).toBe("");
    expect(orgFromHost("app.hazyflow.app", "hazyflow.app")).toBe("");
    expect(orgFromHost("api.hazyflow.app", "hazyflow.app")).toBe("");
  });

  it("returns empty for malformed labels", () => {
    expect(orgFromHost("-bad.hazyflow.app", "hazyflow.app")).toBe("");
    expect(orgFromHost("bad-.hazyflow.app", "hazyflow.app")).toBe("");
  });

  it("tolerates a leading dot on the configured domain", () => {
    expect(orgFromHost("acme.hazyflow.app", ".hazyflow.app")).toBe("acme");
  });
});
