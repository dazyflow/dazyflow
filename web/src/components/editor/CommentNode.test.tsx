// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { CommentNode, FRAME_COLORS, FRAME_COLOR_DEFAULT, type CommentData } from "./CommentNode";
import type { NodeProps } from "@xyflow/react";

// The real i18n bundle is loaded (not stubbed): a swatch's only accessible
// name is its colour name, so a missing key would leave six buttons that a
// screen reader reads as nothing, and that is exactly what this asserts on.
vi.mock("@xyflow/react", () => ({
  // The resizer draws canvas-only drag handles; it needs a React Flow store.
  NodeResizer: () => null,
}));

function renderNode(data: CommentData, selected = true) {
  return render(
    <CommentNode {...({ data, selected } as unknown as NodeProps)} />,
  );
}

describe("CommentNode colour", () => {
  it("offers a swatch per palette colour, naming each one", () => {
    renderNode({ onColorChange: () => {}, onTitleChange: () => {} });
    const group = screen.getByRole("group", { name: "Comment colour" });
    const swatches = [...group.querySelectorAll("button.dz-frame-swatch")];
    expect(swatches).toHaveLength(FRAME_COLORS.length);
    // Every swatch carries its own name, and no two share one.
    const names = swatches.map((b) => b.getAttribute("aria-label") ?? "");
    expect(names.filter((n) => n.trim() !== "")).toHaveLength(FRAME_COLORS.length);
    expect(new Set(names).size).toBe(FRAME_COLORS.length);
  });

  it("marks the note's current colour as the pressed one", () => {
    renderNode({ color: FRAME_COLORS[2].hex, onColorChange: () => {} });
    const pressed = screen
      .getByRole("group", { name: "Comment colour" })
      .querySelectorAll('button[aria-pressed="true"]');
    expect(pressed).toHaveLength(1);
    expect((pressed[0] as HTMLElement).style.background).toBeTruthy();
    expect(pressed[0].className).toContain("active");
  });

  it("treats a colour saved in a different case as the same colour", () => {
    // Frames round-trip through the graph JSON; nothing normalizes the hex on
    // the way in, so an upper-case value must not read as "no colour picked".
    renderNode({ color: FRAME_COLOR_DEFAULT.toUpperCase(), onColorChange: () => {} });
    const pressed = screen
      .getByRole("group", { name: "Comment colour" })
      .querySelectorAll('button[aria-pressed="true"]');
    expect(pressed).toHaveLength(1);
  });

  it("reports the picked colour and doesn't let the click reach the canvas", () => {
    const onColorChange = vi.fn();
    renderNode({ onColorChange });
    const target = FRAME_COLORS[3];
    const btn = [...document.querySelectorAll("button.dz-frame-swatch")][3] as HTMLElement;
    const click = new MouseEvent("click", { bubbles: true, cancelable: true });
    const seenByCanvas = vi.fn();
    document.addEventListener("click", seenByCanvas);
    btn.dispatchEvent(click);
    document.removeEventListener("click", seenByCanvas);

    expect(onColorChange).toHaveBeenCalledWith(target.hex);
    // A swatch click that bubbles also re-selects and can start dragging the
    // frame under the pointer.
    expect(seenByCanvas).not.toHaveBeenCalled();
  });

  it("hides the tools until the note is selected", () => {
    renderNode({ onColorChange: () => {}, onRequestDelete: () => {} }, false);
    expect(screen.queryByRole("group", { name: "Comment colour" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete comment" })).toBeNull();
  });

  it("hides the tools on a canvas that can't be edited", () => {
    // FlowEditor injects the callbacks; a read-only canvas passes none, and
    // the row must not offer a choice that goes nowhere.
    renderNode({ title: "Intake" });
    expect(screen.queryByRole("group", { name: "Comment colour" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete comment" })).toBeNull();
  });
});
