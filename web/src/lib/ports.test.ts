import { describe, it, expect } from "vitest";
import type { Port } from "../types";
import { mimeCompatible, pickPort } from "./ports";

describe("mimeCompatible", () => {
  it("treats an untyped side as anything", () => {
    expect(mimeCompatible(undefined, ["text/plain"])).toBe(true);
    expect(mimeCompatible(["text/plain"], [])).toBe(true);
  });
  it("requires an exact MIME overlap (mirrors the backend validator)", () => {
    expect(mimeCompatible(["text/plain"], ["text/plain"])).toBe(true);
    // Same family is NOT enough — must be the exact same MIME.
    expect(mimeCompatible(["text/markdown"], ["text/plain"])).toBe(false);
    expect(mimeCompatible(["application/json"], ["application/x-bool"])).toBe(false);
    expect(mimeCompatible(["image/png"], ["text/plain"])).toBe(false);
    // Overlap anywhere in the sets still connects.
    expect(mimeCompatible(["text/plain", "application/json"], ["application/json"])).toBe(true);
  });
});

describe("pickPort", () => {
  // ntfy as the engine surfaces it: WithPassthrough prepends an untyped
  // "pass" pin ahead of the real "body" input (labelled "Message"). Both
  // are untyped, so the old "first compatible port" logic landed on pass.
  const ntfyInputs: Port[] = [
    { port: "pass", label: "Pass-through" },
    { port: "body", label: "Message" },
  ];

  it("attaches a Text output to ntfy's Message input, not passthrough", () => {
    expect(pickPort(ntfyInputs, ["text/plain"], "in")).toBe("body");
  });

  it("prefers a real input even when the dragged port is untyped", () => {
    expect(pickPort(ntfyInputs, undefined, "in")).toBe("body");
  });

  it("prefers a strict MIME match over an untyped real input", () => {
    const inputs: Port[] = [
      { port: "pass" },
      { port: "extra" }, // untyped real input, listed first
      { port: "msg", mime: ["text/plain"] },
    ];
    expect(pickPort(inputs, ["text/plain"], "in")).toBe("msg");
  });

  it("falls back to the passthrough pin when no real input is compatible", () => {
    const inputs: Port[] = [
      { port: "pass" },
      { port: "img", mime: ["image/png"] },
    ];
    // A text source matches nothing real → pass (untyped) is the only fit.
    expect(pickPort(inputs, ["text/plain"], "in")).toBe("pass");
  });

  it("returns the fallback handle id when the drop has no ports", () => {
    expect(pickPort([], ["text/plain"], "in")).toBe("in");
    expect(pickPort(undefined, ["text/plain"], "out")).toBe("out");
  });
});
