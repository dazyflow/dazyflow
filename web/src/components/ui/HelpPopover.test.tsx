// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HelpPopover } from "./HelpPopover";

const BODY =
  "Run the loop body once per item in an input list. Items execute in parallel " +
  "up to the concurrency setting.";

function setup() {
  render(<HelpPopover label="What this step does" body={BODY} />);
  return screen.getByRole("button", { name: "What this step does" });
}

describe("HelpPopover", () => {
  // The whole reason this replaced `title=`: a native tooltip never fires on
  // touch, so on a tablet this content did not exist. A real click target is
  // what makes it reachable, so that is what the tests hold onto.
  it("opens the body on click and closes on a second click", async () => {
    const user = userEvent.setup();
    const btn = setup();
    expect(screen.queryByText(BODY)).toBeNull();

    await user.click(btn);
    expect(screen.getByRole("note")).toHaveTextContent(BODY);

    await user.click(btn);
    expect(screen.queryByText(BODY)).toBeNull();
  });

  it("advertises its state so a screen reader can announce it", async () => {
    const user = userEvent.setup();
    const btn = setup();
    expect(btn).toHaveAttribute("aria-expanded", "false");
    await user.click(btn);
    expect(btn).toHaveAttribute("aria-expanded", "true");
    expect(btn).toHaveAttribute("aria-controls", screen.getByRole("note").id);
  });

  // The body must NOT also sit in `title`: a native tooltip would flicker over
  // the panel, and a screen reader would read the same paragraph twice.
  it("keeps only the short label in title, never the body", async () => {
    const user = userEvent.setup();
    const btn = setup();
    expect(btn).toHaveAttribute("title", "What this step does");
    await user.click(btn);
    expect(btn.getAttribute("title")).not.toContain("loop body");
  });

  it("closes on Escape and hands focus back to the trigger", async () => {
    const user = userEvent.setup();
    const btn = setup();
    await user.click(btn);
    await user.keyboard("{Escape}");
    expect(screen.queryByText(BODY)).toBeNull();
    expect(btn).toHaveFocus();
  });

  // Escape inside the popover must not also reach the Inspector behind it —
  // one press should not close the panel you were reading about.
  it("does not let Escape through to an ancestor handler", async () => {
    const user = userEvent.setup();
    const onKey = vi.fn();
    render(
      <div onKeyDown={onKey}>
        <HelpPopover label="About this field" body={BODY} />
      </div>,
    );
    await user.click(screen.getByRole("button", { name: "About this field" }));
    await user.keyboard("{Escape}");
    expect(onKey).not.toHaveBeenCalled();
  });

  // The panel used to be an absolute child of the (i), which meant the
  // inspector's scroll container clipped it and the editor canvas painted over
  // it. Portaled to <body> with fixed coords, neither can happen — so where it
  // renders is behaviour worth holding onto, not an implementation detail.
  it("renders the panel at document.body, outside the trigger's subtree", async () => {
    const user = userEvent.setup();
    const btn = setup();
    await user.click(btn);
    const note = screen.getByRole("note");
    expect(note.parentElement).toBe(document.body);
    expect(btn.closest(".help-pop-wrap")?.contains(note)).toBe(false);
    expect(note).toHaveStyle({ position: "fixed" });
  });

  // Reading a long description means scrolling it; a pointerdown inside the
  // portaled panel is not an "outside" click even though it isn't a
  // descendant of the trigger.
  it("stays open when the pointer goes down inside the panel", async () => {
    const user = userEvent.setup();
    const btn = setup();
    await user.click(btn);
    await user.click(screen.getByRole("note"));
    expect(screen.getByRole("note")).toHaveTextContent(BODY);
  });

  it("closes when the pointer goes down outside it", async () => {
    const user = userEvent.setup();
    render(
      <div>
        <HelpPopover label="What this step does" body={BODY} />
        <button type="button">elsewhere</button>
      </div>,
    );
    await user.click(screen.getByRole("button", { name: "What this step does" }));
    expect(screen.getByRole("note")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "elsewhere" }));
    expect(screen.queryByText(BODY)).toBeNull();
  });
});
