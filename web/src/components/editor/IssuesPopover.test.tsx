// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { IssuesButton, IssueRow } from "./IssuesPopover";

// A caller that owns `open` the way the editor does, so a click goes through
// the same controlled path rather than internal state the editor cannot see.
function Harness({
  count = 2,
  onAction,
}: {
  count?: number;
  onAction?: () => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <IssuesButton
      kind="warning"
      count={count}
      title="2 warnings"
      heading="Warnings"
      open={open}
      onOpenChange={setOpen}
    >
      <IssueRow
        title="lint_code"
        text="Items can't plug into a Text input"
        actions={
          <button type="button" onClick={onAction}>
            Fix it
          </button>
        }
      />
      <IssueRow text="Gmail needs connecting" />
    </IssuesButton>
  );
}

describe("the editor's issue buttons", () => {
  it("shows the count and nothing else until it is asked", async () => {
    render(<Harness />);
    expect(screen.getByRole("button", { name: "2 warnings" })).toBeInTheDocument();
    // The whole point of the change: the words are not over the canvas.
    expect(
      screen.queryByText("Items can't plug into a Text input"),
    ).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "2 warnings" }));
    expect(
      await screen.findByText("Items can't plug into a Text input"),
    ).toBeInTheDocument();
    expect(screen.getByText("Gmail needs connecting")).toBeInTheDocument();
  });

  it("renders nothing at all when there is nothing to report", () => {
    render(<Harness count={0} />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("keeps a row's own action working inside the panel", async () => {
    const onAction = vi.fn();
    render(<Harness onAction={onAction} />);
    await userEvent.click(screen.getByRole("button", { name: "2 warnings" }));
    await userEvent.click(await screen.findByText("Fix it"));
    // A row's button must not be mistaken for a click outside the panel, which
    // dismisses it: the panel is portaled, so it is not a DOM descendant of the
    // trigger and both have to be checked.
    expect(onAction).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Gmail needs connecting")).toBeInTheDocument();
  });

  it("closes on Escape and on a click outside", async () => {
    render(<Harness />);
    const button = screen.getByRole("button", { name: "2 warnings" });

    await userEvent.click(button);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    await userEvent.click(button);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    await userEvent.click(document.body);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("says which panel is open, for a screen reader and for the tone", async () => {
    render(<Harness />);
    const button = screen.getByRole("button", { name: "2 warnings" });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveAttribute("data-kind", "warning");

    await userEvent.click(button);
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(await screen.findByRole("dialog", { name: "Warnings" })).toBeInTheDocument();
  });
});
