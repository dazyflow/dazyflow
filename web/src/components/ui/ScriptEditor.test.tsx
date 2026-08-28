// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ScriptEditor } from "./ScriptEditor";

describe("ScriptEditor", () => {
  it("renders the highlight as escaped React spans, never as live markup", () => {
    // This text is a script from a flow the reader may not have written, and it
    // ends up executed on a machine they own. It must not also be able to put
    // an element into the page that shows it.
    const { container } = render(
      <ScriptEditor
        value={'echo "<img src=x onerror=alert(1)>"'}
        onChange={() => {}}
        lang="shell"
      />,
    );
    expect(container.querySelector(".dz-s-keyword")).toHaveTextContent("echo");
    expect(container.querySelector("img")).toBeNull();
    expect(container.textContent).toContain("<img src=x onerror=alert(1)>");
  });

  it("indents with Tab instead of leaving the field", async () => {
    // In a one-line input Tab moving on is right; in a code box it is how you
    // lose your place, and Python is not writable without indentation.
    const onChange = vi.fn();
    render(<ScriptEditor value="" onChange={onChange} lang="python" />);
    const ta = screen.getByRole("textbox");
    await userEvent.click(ta);
    await userEvent.keyboard("{Tab}");
    expect(onChange).toHaveBeenCalledWith("  ");
    expect(ta).toHaveFocus();
  });

  it("still lets Shift+Tab move focus on, so the field is not a keyboard trap", async () => {
    const onChange = vi.fn();
    render(
      <>
        <button>before</button>
        <ScriptEditor value="" onChange={onChange} lang="python" />
      </>,
    );
    const ta = screen.getByRole("textbox");
    await userEvent.click(ta);
    await userEvent.keyboard("{Shift>}{Tab}{/Shift}");
    expect(onChange).not.toHaveBeenCalled();
    expect(ta).not.toHaveFocus();
  });
});
