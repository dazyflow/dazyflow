// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Modal } from "./Modal";

describe("Modal", () => {
  it("portals to <body> and is announced with a name", () => {
    const { container } = render(
      <div style={{ transform: "scale(1)" }}>
        <Modal title="Hello" onClose={() => {}}>
          body
        </Modal>
      </div>,
    );
    const dialog = screen.getByRole("dialog", { name: "Hello" });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    // Not inside the transformed wrapper, which would trap position:fixed.
    expect(container.contains(dialog)).toBe(false);
    expect(dialog.parentElement?.parentElement).toBe(document.body);
  });

  it("closes on Escape and backdrop click, not on a click inside", async () => {
    const onClose = vi.fn();
    render(
      <Modal title="Hello" onClose={onClose}>
        <button>inside</button>
      </Modal>,
    );
    await userEvent.click(screen.getByText("inside"));
    expect(onClose).not.toHaveBeenCalled();
    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
    await userEvent.click(document.querySelector(".modal-backdrop")!);
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("ignores Escape and backdrop while not dismissible", async () => {
    const onClose = vi.fn();
    render(
      <Modal title="Hello" onClose={onClose} dismissible={false}>
        body
      </Modal>,
    );
    await userEvent.keyboard("{Escape}");
    await userEvent.click(document.querySelector(".modal-backdrop")!);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("renders a form panel when onSubmit is given", () => {
    render(
      <Modal labelledBy="h" onClose={() => {}} onSubmit={(e) => e.preventDefault()}>
        <h2 id="h">Named by heading</h2>
      </Modal>,
    );
    const dialog = screen.getByRole("dialog", { name: "Named by heading" });
    expect(dialog.tagName).toBe("FORM");
  });
});
