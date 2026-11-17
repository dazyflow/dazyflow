// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The copy affordance used to be written out per page, and four of those
// copies said "Copied" whether or not the clipboard write worked. These pin the
// one rule that matters: the tick follows a successful write, and only that.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { CopyButton } from "./CopyButton";
import { FEEDBACK } from "../../lib/timing";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function stubClipboard(writeText: (s: string) => Promise<void>) {
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: vi.fn(writeText) },
  });
  return navigator.clipboard.writeText as ReturnType<typeof vi.fn>;
}

afterEach(() => {
  vi.useRealTimers();
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
});

describe("CopyButton", () => {
  it("writes the text and shows Copied until FEEDBACK.copied has passed", async () => {
    vi.useFakeTimers();
    const write = stubClipboard(() => Promise.resolve());
    render(<CopyButton text="hello" />);
    await act(async () => {
      fireEvent.click(screen.getByRole("button"));
    });
    expect(write).toHaveBeenCalledWith("hello");
    expect(screen.getByRole("button")).toHaveTextContent("common.copied");
    act(() => {
      vi.advanceTimersByTime(FEEDBACK.copied);
    });
    expect(screen.getByRole("button")).toHaveTextContent("common.copy");
  });

  it("does not claim Copied when the clipboard write is refused", async () => {
    stubClipboard(() => Promise.reject(new Error("denied")));
    render(<CopyButton text="hello" label="Copy link" />);
    await act(async () => {
      fireEvent.click(screen.getByRole("button"));
    });
    expect(screen.getByRole("button")).toHaveTextContent("Copy link");
  });

  it("does not claim Copied when there is no clipboard at all", async () => {
    render(<CopyButton text="hello" />);
    await act(async () => {
      fireEvent.click(screen.getByRole("button"));
    });
    expect(screen.getByRole("button")).toHaveTextContent("common.copy");
  });

  it("labels an icon-only button for screen readers", () => {
    render(<CopyButton text="x" size="icon" label="Copy run ID" />);
    expect(screen.getByRole("button", { name: "Copy run ID" })).toBeInTheDocument();
  });
});
