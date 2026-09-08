// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CanvasContextMenu, type ContextMenuItem } from "./CanvasContextMenu";


const show = (items: ContextMenuItem[], onClose = () => {}) =>
  render(<CanvasContextMenu x={10} y={10} items={items} onClose={onClose} />);

describe("CanvasContextMenu", () => {
  it("renders a checkable group as radio items, with one selected", async () => {
    show([
      { header: "When does the next step run?" },
      { label: "Only if it succeeds", checked: true, onClick: () => {} },
      { label: "Only if it fails", checked: false, onClick: () => {} },
    ]);
    const options = screen.getAllByRole("menuitemradio");
    expect(options).toHaveLength(2);
    expect(options[0]).toHaveAttribute("aria-checked", "true");
    expect(options[1]).toHaveAttribute("aria-checked", "false");
  });

  it("keeps a plain command a plain menuitem", async () => {
    show([{ label: "Delete", onClick: () => {} }]);
    expect(screen.getByRole("menuitem", { name: "Delete" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitemradio")).not.toBeInTheDocument();
  });

  it("shows the header without making it clickable", () => {
    show([{ header: "When does the next step run?" }, { label: "Go", onClick: () => {} }]);
    expect(screen.getByText("When does the next step run?")).toBeInTheDocument();
    expect(screen.getAllByRole("menuitem")).toHaveLength(1);
  });

  it("explains a disabled item instead of just greying it", () => {
    show([
      { label: "Retry this step", disabled: true, title: "This step can't be retried", onClick: () => {} },
    ]);
    const item = screen.getByRole("menuitem", { name: "Retry this step" });
    expect(item).toBeDisabled();
    expect(item).toHaveAttribute("title", "This step can't be retried");
  });

  it("runs the item and closes, once", async () => {
    const onClick = vi.fn();
    const onClose = vi.fn();
    show([{ label: "Only if it fails", checked: false, onClick }], onClose);
    await userEvent.click(screen.getByRole("menuitemradio", { name: "Only if it fails" }));
    expect(onClick).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalled();
  });
});
