// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, vi } from "vitest";
import { clearTestEvent, loadTestEvent, saveTestEvent } from "./testEventStore";

// withLocalStorage swaps the global for the duration of fn and puts the real
// one back afterwards, however fn ends.
function withLocalStorage(stub: Partial<Storage>, fn: () => void) {
  const real = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    value: {
      getItem: () => null,
      setItem: () => {},
      removeItem: () => {},
      ...stub,
    },
    configurable: true,
  });
  try {
    fn();
  } finally {
    if (real) Object.defineProperty(globalThis, "localStorage", real);
  }
}

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("testEventStore", () => {
  it("hands back the payload the flow was last tested with", () => {
    saveTestEvent("coffee", '{"order":"4471"}');
    expect(loadTestEvent("coffee")).toBe('{"order":"4471"}');
  });

  it("keeps flows apart", () => {
    saveTestEvent("coffee", '{"a":1}');
    saveTestEvent("refunds", '{"b":2}');
    expect(loadTestEvent("coffee")).toBe('{"a":1}');
    expect(loadTestEvent("refunds")).toBe('{"b":2}');
    expect(loadTestEvent("never-tested")).toBeNull();
  });

  it("forgets on reset, so the dialog goes back to a generated sample", () => {
    saveTestEvent("coffee", '{"a":1}');
    clearTestEvent("coffee");
    expect(loadTestEvent("coffee")).toBeNull();
  });

  it("treats an emptied box as nothing to restore", () => {
    saveTestEvent("coffee", '{"a":1}');
    saveTestEvent("coffee", "   ");
    // Restoring "" would read as the feature having lost the edit.
    expect(loadTestEvent("coffee")).toBeNull();
  });

  it("does nothing for a flow with no id yet", () => {
    expect(() => saveTestEvent(undefined, '{"a":1}')).not.toThrow();
    expect(loadTestEvent(undefined)).toBeNull();
    expect(loadTestEvent("")).toBeNull();
  });

  it("declines to spend the storage budget on an enormous body", () => {
    // /test-trigger takes up to 1 MiB; localStorage is ~5 MB for the whole
    // app. The payload still fires, it just isn't remembered.
    saveTestEvent("coffee", JSON.stringify({ blob: "x".repeat(300 * 1024) }));
    expect(loadTestEvent("coffee")).toBeNull();
  });

  it("survives storage being unavailable", () => {
    // A private window, or a browser set to block site data: reads and writes
    // both throw, and neither may take the dialog down with them. Replacing
    // the whole object rather than spying on Storage.prototype — jsdom's
    // localStorage doesn't route through the prototype, so a spy there is
    // silently ignored and the test passes against a broken guard.
    const boom = () => {
      throw new Error("SecurityError");
    };
    withLocalStorage({ getItem: boom, setItem: boom, removeItem: boom }, () => {
      expect(() => saveTestEvent("coffee", '{"a":1}')).not.toThrow();
      expect(loadTestEvent("coffee")).toBeNull();
      expect(() => clearTestEvent("coffee")).not.toThrow();
    });
  });

  it("survives a full quota", () => {
    withLocalStorage(
      {
        setItem: () => {
          throw new Error("QuotaExceededError");
        },
      },
      () => {
        expect(() => saveTestEvent("coffee", '{"a":1}')).not.toThrow();
      },
    );
  });
});
