// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The run-succeeded readout, now a toolbar item that fades rather than a panel
// over the canvas.
//
// Two of these pin behaviour that is easy to break and invisible when it is:
// the timer must survive a re-render (the caller passes a fresh onDismiss every
// time, so a naive dependency array restarts the countdown forever and it never
// fires), and it must pause while someone is reaching for the link.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  return { useTranslation: () => ({ t, i18n: { language: "en" } }) };
});

import { RunSucceededStatus } from "./RunSucceededStatus";

function show(onDismiss = () => {}, label = "Send notification") {
  return render(
    <MemoryRouter>
      <RunSucceededStatus run={{ runID: "run-1", label }} onDismiss={onDismiss} />
    </MemoryRouter>,
  );
}

const wait = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
afterEach(() => vi.useRealTimers());

describe("the run-succeeded status", () => {
  it("says it worked, and never puts port data on screen", () => {
    show();
    expect(screen.getByText("editor.runSucceededWith")).toBeInTheDocument();
    // The whole panel is gone, so the JSON scroll box cannot come back by
    // accident: there is no toggle and nothing to expand.
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("keeps the way to the full run", () => {
    show();
    expect(
      screen.getByRole("link", { name: "editor.runSucceededDetails" }),
    ).toHaveAttribute("href", "/runs/run-1");
  });

  it("fades on its own", async () => {
    const onDismiss = vi.fn();
    show(onDismiss);
    await wait(5000);
    expect(onDismiss).not.toHaveBeenCalled();
    await wait(1500);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("still fades when the parent re-renders with a new callback each time", async () => {
    const onDismiss = vi.fn();
    const { rerender } = show(onDismiss);
    for (let i = 0; i < 6; i++) {
      await wait(500);
      rerender(
        <MemoryRouter>
          <RunSucceededStatus
            run={{ runID: "run-1", label: "Send notification" }}
            onDismiss={() => onDismiss()}
          />
        </MemoryRouter>,
      );
    }
    await wait(4000);
    expect(onDismiss).toHaveBeenCalled();
  });

  it("holds while the pointer is on it, and resumes on the way out", async () => {
    const onDismiss = vi.fn();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    show(onDismiss);
    const status = screen.getByRole("status");

    await user.hover(status);
    await wait(9000);
    expect(onDismiss).not.toHaveBeenCalled();

    await user.unhover(status);
    await wait(6500);
    expect(onDismiss).toHaveBeenCalled();
  });

  it("restarts the countdown for a new run", async () => {
    const onDismiss = vi.fn();
    const { rerender } = show(onDismiss);
    await wait(5000);
    rerender(
      <MemoryRouter>
        <RunSucceededStatus run={{ runID: "run-2", label: "" }} onDismiss={onDismiss} />
      </MemoryRouter>,
    );
    // The first run's remaining second must not carry over and dismiss the
    // second run's message a beat after it appeared.
    await wait(1500);
    expect(onDismiss).not.toHaveBeenCalled();
    await wait(5000);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });
});
