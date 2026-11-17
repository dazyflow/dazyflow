import { describe, expect, it } from "vitest";
import { isTypingTarget } from "./typingTarget";

describe("isTypingTarget", () => {
  it("is true for inputs and textareas", () => {
    expect(isTypingTarget(document.createElement("input"))).toBe(true);
    expect(isTypingTarget(document.createElement("textarea"))).toBe(true);
  });

  it("is true for contenteditable elements", () => {
    const div = document.createElement("div");
    div.contentEditable = "true";
    document.body.appendChild(div);
    // jsdom does not compute isContentEditable from the attribute.
    Object.defineProperty(div, "isContentEditable", { value: true });
    expect(isTypingTarget(div)).toBe(true);
    div.remove();
  });

  it("is false for other elements and non-elements", () => {
    expect(isTypingTarget(document.createElement("button"))).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
    expect(isTypingTarget(undefined)).toBe(false);
    expect(isTypingTarget(window)).toBe(false);
  });
});
