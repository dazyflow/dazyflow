// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The field, given the whole window. What matters here is that it is the SAME
// field: one value, edited live, with no draft to lose — so closing the window
// by any of the three ways out can never be the wrong move.

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "en" } }),
}));

import { ExpandedEditor } from "./ExpandedEditor";

const open = async () =>
  await userEvent.click(screen.getByRole("button", { name: /schemaForm.expand/ }));

describe("ExpandedEditor", () => {
  it("opens a dialog headed by the field it came from", async () => {
    render(<ExpandedEditor title="Template" value="" lang="html" onChange={() => {}} />);
    expect(screen.queryByRole("dialog")).toBeNull();
    await open();
    expect(screen.getByRole("dialog", { name: "Template" })).toBeInTheDocument();
  });

  it("edits the same value the field holds", async () => {
    const onChange = vi.fn();
    render(<ExpandedEditor title="Template" value="<p>" lang="html" onChange={onChange} />);
    await open();
    await userEvent.type(screen.getByRole("textbox"), "!");
    expect(onChange).toHaveBeenCalledWith("<p>!");
  });

  it("colours the text only when the caller says what language it is", async () => {
    const { container, unmount } = render(
      <ExpandedEditor title="Template" value="<p>hi</p>" lang="html" onChange={() => {}} />,
    );
    await open();
    expect(container.ownerDocument.querySelector(".dz-code-editor")).not.toBeNull();
    unmount();

    render(<ExpandedEditor title="Body" value="Dear friend" onChange={() => {}} />);
    await open();
    // Prose: a plain box. A monospace face and syntax colours make an email
    // body harder to read, not easier.
    expect(document.querySelector(".dz-code-editor")).toBeNull();
    expect(document.querySelector(".dz-bigedit-plain")).not.toBeNull();
  });

  it("closes on Escape, on the backdrop, and on its own close button", async () => {
    render(<ExpandedEditor title="Template" value="" lang="html" onChange={() => {}} />);

    await open();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();

    await open();
    await userEvent.click(document.querySelector(".modal-backdrop")!);
    expect(screen.queryByRole("dialog")).toBeNull();

    await open();
    await userEvent.click(screen.getByRole("button", { name: "common.close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // Step 3's whole point: the markup on the left, what it comes out as on the
  // right, in one window. Without a preview the editor keeps the whole width.
  it("shows a preview beside the editor when the field has one", async () => {
    render(
      <ExpandedEditor
        title="Template"
        value="<p>hi</p>"
        lang="html"
        onChange={() => {}}
        preview={<span data-testid="preview">rendered</span>}
      />,
    );
    await open();
    expect(screen.getByTestId("preview")).toBeInTheDocument();
    expect(document.querySelector(".dz-bigedit-split")).not.toBeNull();
  });

  it("gives the editor the whole window when there is nothing to show beside it", async () => {
    render(<ExpandedEditor title="Script" value="x" lang="js" onChange={() => {}} />);
    await open();
    expect(document.querySelector(".dz-bigedit-split")).toBeNull();
  });

  it("puts the caret in the editor, not somewhere behind it", async () => {
    render(<ExpandedEditor title="Template" value="x" lang="html" onChange={() => {}} />);
    await open();
    expect(screen.getByRole("textbox")).toHaveFocus();
  });
});
