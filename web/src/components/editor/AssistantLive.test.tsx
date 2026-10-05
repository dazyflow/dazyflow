// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The live view of an assistant building the open flow: which nodes light up,
// what the ticker says, and that both go away on their own.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, renderHook, screen } from "@testing-library/react";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k} ${JSON.stringify(o)}` : k),
  }),
}));

import {
  ASSISTANT_CHANGED_MS,
  ASSISTANT_TICKER_MS,
  AssistantTicker,
  useAssistantLive,
} from "./AssistantLive";

afterEach(() => vi.useRealTimers());

describe("useAssistantLive", () => {
  it("lights what the assistant touched, tells new from changed, then clears", () => {
    vi.useFakeTimers();
    const { result } = renderHook(() => useAssistantLive());
    act(() =>
      result.current.announce(
        { assistant: true, note: "Post orders to #sales", touched: ["post", "hook"] },
        new Set(["hook"]),
      ),
    );
    expect([...result.current.touched]).toEqual(["post", "hook"]);
    expect(result.current.ticker[0]).toMatchObject({ added: 1, changed: 1, note: "Post orders to #sales" });

    render(<AssistantTicker entries={result.current.ticker} />);
    expect(screen.getByText(/editor.assistant.added .*editor.assistant.alsoChanged/)).toBeTruthy();
    expect(screen.getByText("Post orders to #sales")).toBeTruthy();

    act(() => vi.advanceTimersByTime(ASSISTANT_CHANGED_MS));
    expect(result.current.touched.size).toBe(0);
    expect(result.current.ticker).toHaveLength(1);
    act(() => vi.advanceTimersByTime(ASSISTANT_TICKER_MS));
    expect(result.current.ticker).toHaveLength(0);
  });

  it("ignores a save that was not an assistant's", () => {
    const { result } = renderHook(() => useAssistantLive());
    act(() => result.current.announce({ touched: ["a"] }, new Set()));
    expect(result.current.touched.size).toBe(0);
    expect(result.current.ticker).toHaveLength(0);
  });
});
