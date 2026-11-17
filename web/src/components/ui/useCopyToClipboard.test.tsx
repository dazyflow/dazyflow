// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The copy affordance used to be written out per file, and four of them
// raised "Copied" without waiting for the write — so a blocked clipboard
// still told the user it had worked. These pin the shared hook to the honest
// behaviour and the one FEEDBACK.copied duration.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useCopyToClipboard } from "./useCopyToClipboard";
import { FEEDBACK } from "../../lib/timing";
import { absoluteURL } from "../../lib/absoluteURL";

function mockClipboard(writeText: (s: string) => Promise<void>) {
  Object.defineProperty(navigator, "clipboard", {
    value: { writeText },
    configurable: true,
  });
}

describe("useCopyToClipboard", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("raises copied after a successful write and drops it after FEEDBACK.copied", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    mockClipboard(writeText);
    const { result } = renderHook(() => useCopyToClipboard());
    await act(async () => {
      expect(await result.current.copy("hello")).toBe(true);
    });
    expect(writeText).toHaveBeenCalledWith("hello");
    expect(result.current.copied).toBe(true);
    act(() => vi.advanceTimersByTime(FEEDBACK.copied));
    expect(result.current.copied).toBe(false);
  });

  it("stays quiet when the clipboard rejects the write", async () => {
    mockClipboard(() => Promise.reject(new Error("denied")));
    const { result } = renderHook(() => useCopyToClipboard());
    await act(async () => {
      expect(await result.current.copy("hello")).toBe(false);
    });
    expect(result.current.copied).toBe(false);
  });

  it("tracks which of several buttons was copied", async () => {
    mockClipboard(() => Promise.resolve());
    const { result } = renderHook(() => useCopyToClipboard<"a" | "b">());
    await act(async () => {
      await result.current.copy("x", "a");
    });
    expect(result.current.copiedKey).toBe("a");
    await act(async () => {
      await result.current.copy("y", "b");
    });
    expect(result.current.copiedKey).toBe("b");
  });
});

describe("absoluteURL", () => {
  it("prefixes a path with the page origin", () => {
    expect(absoluteURL("/invite/abc")).toBe(window.location.origin + "/invite/abc");
  });
  it("leaves an absolute URL alone", () => {
    expect(absoluteURL("https://example.com/x")).toBe("https://example.com/x");
  });
});
