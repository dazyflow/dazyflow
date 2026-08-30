// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { approvalContextView } from "./approvalContext";

describe("approvalContextView", () => {
  it("shows a form submission as named fields, in the order they were declared", () => {
    // The case that started this: an approver was shown a step id instead of
    // the person and what they wrote.
    const view = approvalContextView({
      "Your name": "Marina Alvarez",
      "Your email": "marina@example.com",
      "What you like about us": "The Earl Grey.",
    });
    expect(view).toEqual({
      kind: "fields",
      more: 0,
      fields: [
        { key: "Your name", value: "Marina Alvarez" },
        { key: "Your email", value: "marina@example.com" },
        { key: "What you like about us", value: "The Earl Grey." },
      ],
    });
  });

  it("unwraps a one-row list", () => {
    // How a form submission reaches a row-writing step.
    expect(approvalContextView([{ name: "Ada" }])).toEqual({
      kind: "fields",
      more: 0,
      fields: [{ key: "name", value: "Ada" }],
    });
  });

  it("shows a drafted reply as text", () => {
    expect(
      approvalContextView("Thanks for writing in — we'll ship today."),
    ).toEqual({
      kind: "text",
      text: "Thanks for writing in — we'll ship today.",
    });
  });

  it("renders nothing when nothing was wired", () => {
    expect(approvalContextView(null)).toBeNull();
    expect(approvalContextView(undefined)).toBeNull();
    expect(approvalContextView("")).toBeNull();
    expect(approvalContextView("   ")).toBeNull();
    expect(approvalContextView({})).toBeNull();
    expect(approvalContextView([])).toBeNull();
  });

  it("drops blank fields rather than showing empty rows", () => {
    const view = approvalContextView({ name: "Ada", phone: "", note: null });
    expect(view).toEqual({
      kind: "fields",
      more: 0,
      fields: [{ key: "name", value: "Ada" }],
    });
  });

  it("caps a wide record so it can't bury the Approve button", () => {
    const wide: Record<string, string> = {};
    for (let i = 0; i < 20; i++) wide[`f${i}`] = `v${i}`;
    const view = approvalContextView(wide);
    if (view?.kind !== "fields") throw new Error("expected fields");
    expect(view.fields).toHaveLength(8);
    expect(view.more).toBe(12);
  });

  it("trims one very long answer instead of letting it crowd out the rest", () => {
    const view = approvalContextView({ note: "x".repeat(500), who: "Ada" });
    if (view?.kind !== "fields") throw new Error("expected fields");
    expect(view.fields[0].value.endsWith("…")).toBe(true);
    expect(view.fields[0].value.length).toBeLessThanOrEqual(301);
    // The later field survives.
    expect(view.fields[1]).toEqual({ key: "who", value: "Ada" });
  });

  it("keeps zero and false, which are real answers", () => {
    const view = approvalContextView({ refunded: false, amount: 0 });
    expect(view).toEqual({
      kind: "fields",
      more: 0,
      fields: [
        { key: "refunded", value: "false" },
        { key: "amount", value: "0" },
      ],
    });
  });

  it("renders a nested value compactly rather than dropping it", () => {
    const view = approvalContextView({ order: { id: 42, total: 230 } });
    if (view?.kind !== "fields") throw new Error("expected fields");
    expect(view.fields[0].value).toBe('{"id":42,"total":230}');
  });

  it("summarizes a multi-row list as text", () => {
    const view = approvalContextView([{ a: 1 }, { a: 2 }]);
    expect(view?.kind).toBe("text");
  });

  it("honours the declared field order over JSON's alphabetical keys", () => {
    // The form asked name, email, then the long question. Go serializes map
    // keys sorted, so without the declared order the card leads with "What
    // you like about us" and buries who wrote it.
    const submission = {
      "What you like about us": "The Earl Grey.",
      "Your email": "marina@example.com",
      "Your name": "Marina Alvarez",
    };
    const order = ["Your name", "Your email", "What you like about us"];
    const view = approvalContextView(submission, order);
    if (view?.kind !== "fields") throw new Error("expected fields");
    expect(view.fields.map((f) => f.key)).toEqual(order);
  });

  it("keeps undeclared extras, after the declared ones", () => {
    // collectFormValues deliberately keeps fields the form didn't declare
    // (utm_source and friends); they must not vanish from the card.
    const view = approvalContextView(
      { utm_source: "facebook", name: "Ada", email: "ada@example.com" },
      ["name", "email"],
    );
    if (view?.kind !== "fields") throw new Error("expected fields");
    expect(view.fields.map((f) => f.key)).toEqual([
      "name",
      "email",
      "utm_source",
    ]);
  });

  it("applies the order through a one-row list too", () => {
    const view = approvalContextView([{ b: "2", a: "1" }], ["b", "a"]);
    if (view?.kind !== "fields") throw new Error("expected fields");
    expect(view.fields.map((f) => f.key)).toEqual(["b", "a"]);
  });
});
