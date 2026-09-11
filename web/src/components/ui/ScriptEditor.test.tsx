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

  // The console tells a reader which line printed a message ("3 │ …"), which
  // is only useful if the editor can be read the same way.
  it("numbers every line of the script", () => {
    const { container } = render(
      <ScriptEditor value={"one\ntwo\nthree"} onChange={() => {}} lang="js" />,
    );
    const gutter = container.querySelector(".dz-code-gutter");
    expect(gutter?.textContent).toBe(" 1\n 2\n 3");
  });

  it("counts a trailing newline as the line it leaves you on", () => {
    const { container } = render(
      <ScriptEditor value={"one\n"} onChange={() => {}} lang="js" />,
    );
    // The textarea shows an empty second line to type on; the gutter numbers it.
    expect(container.querySelector(".dz-code-gutter")?.textContent).toBe(" 1\n 2");
  });

  it("widens the column when the numbers do", () => {
    const { container } = render(
      <ScriptEditor
        value={Array.from({ length: 120 }, (_, i) => `line ${i}`).join("\n")}
        onChange={() => {}}
        lang="js"
      />,
    );
    const gutter = container.querySelector(".dz-code-gutter");
    expect(gutter?.textContent?.split("\n").at(-1)).toBe("120");
    // Three digits' worth of column, and the code padded clear of it — read off
    // the editor rather than the gutter, since one variable sets both.
    expect(container.querySelector<HTMLElement>(".dz-code-editor")?.style.getPropertyValue("--dz-gutter-w")).toBe("calc(3ch + 14px)");
  });

  // The textarea already carries the script; a screen reader that also walked
  // the gutter would read a column of bare numbers over it.
  it("keeps the numbers out of the accessibility tree", () => {
    const { container } = render(
      <ScriptEditor value={"one\ntwo"} onChange={() => {}} lang="js" />,
    );
    expect(container.querySelector(".dz-code-gutter")).toHaveAttribute("aria-hidden", "true");
  });

  it("indents with Tab instead of leaving the field", async () => {
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
