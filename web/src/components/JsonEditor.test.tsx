import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { JsonEditor, isInvalidJSON } from "./JsonEditor";

describe("JsonEditor", () => {
  it("renders highlighted tokens as escaped React spans (no innerHTML)", () => {
    const { container } = render(
      <JsonEditor value={'{"k": "<img src=x onerror=alert(1)>"}'} onChange={() => {}} />,
    );
    // The key is coloured as a token...
    expect(container.querySelector(".dz-j-key")).toHaveTextContent('"k"');
    // ...and the malicious value is rendered as text, never as a live element.
    expect(container.querySelector("img")).toBeNull();
    expect(container.textContent).toContain("<img src=x onerror=alert(1)>");
  });

  it("reflects the current value in the textarea and reports edits", async () => {
    const onChange = vi.fn();
    render(<JsonEditor value="{}" onChange={onChange} />);
    const ta = screen.getByRole("textbox");
    expect(ta).toHaveValue("{}");
    await userEvent.type(ta, "x");
    expect(onChange).toHaveBeenCalled();
  });
});

describe("isInvalidJSON", () => {
  it("treats empty/whitespace as valid and bad JSON as invalid", () => {
    expect(isInvalidJSON("")).toBe(false);
    expect(isInvalidJSON("   ")).toBe(false);
    expect(isInvalidJSON('{"a":1}')).toBe(false);
    expect(isInvalidJSON("{not json")).toBe(true);
  });
});
