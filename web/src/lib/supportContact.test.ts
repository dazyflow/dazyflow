// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from "vitest";
import { supportContactHref, supportContactWithContext } from "./supportContact";

describe("supportContactHref", () => {
  it("returns undefined for empty / whitespace / unusable input", () => {
    expect(supportContactHref(undefined)).toBeUndefined();
    expect(supportContactHref("")).toBeUndefined();
    expect(supportContactHref("   ")).toBeUndefined();
    expect(supportContactHref("not a contact")).toBeUndefined();
    expect(supportContactHref("tel:+123")).toBeUndefined();
  });

  it("wraps a bare email as mailto:", () => {
    expect(supportContactHref("support@acme.com")).toBe("mailto:support@acme.com");
    expect(supportContactHref("  help@acme.io  ")).toBe("mailto:help@acme.io");
  });

  it("passes through http(s) and mailto URLs unchanged", () => {
    expect(supportContactHref("https://acme.com/help")).toBe("https://acme.com/help");
    expect(supportContactHref("http://acme.com/help")).toBe("http://acme.com/help");
    expect(supportContactHref("mailto:support@acme.com")).toBe("mailto:support@acme.com");
  });
});

describe("supportContactWithContext", () => {
  const ctx = { subject: "Help with flow \"My Flow\"", body: "line 1\nline 2" };

  it("returns undefined when there is no usable contact", () => {
    expect(supportContactWithContext(undefined, ctx)).toBeUndefined();
    expect(supportContactWithContext("nonsense", ctx)).toBeUndefined();
  });

  it("prefills subject and body on a mailto contact, percent-encoded", () => {
    const href = supportContactWithContext("support@acme.com", ctx);
    expect(href).toBeDefined();
    expect(href!.startsWith("mailto:support@acme.com?")).toBe(true);
    // spaces/newlines encode as %20 / %0A, never `+` (mail clients render
    // a literal +).
    expect(href).toContain("subject=Help%20with%20flow");
    expect(href).toContain("body=line%201%0Aline%202");
    expect(href).not.toContain("+");
  });

  it("appends with & when the mailto already carries a query", () => {
    const href = supportContactWithContext("mailto:s@acme.com?cc=ops@acme.com", ctx);
    expect(href!.includes("?cc=ops@acme.com&subject=")).toBe(true);
  });

  it("returns a URL contact bare (can't prefill an arbitrary form)", () => {
    expect(supportContactWithContext("https://acme.com/help", ctx)).toBe(
      "https://acme.com/help",
    );
  });
});
