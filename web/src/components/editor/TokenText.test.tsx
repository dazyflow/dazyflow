// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Raw ${…} syntax must never reach a node card. The card used to chip a value
// that was ENTIRELY one reference and fall back to plain text otherwise — so
// the exact shape the {} menu produces, a reference inserted into a sentence,
// was the shape that leaked the syntax.

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("../../i18n", () => ({
  default: { t: (k: string) => k.replace(/^tokenChip\./, "") },
}));

import { TokenText } from "./TokenText";

const labels = { "date_1.out": "Date & time · Formatted" };

describe("TokenText", () => {
  it("chips a whole-value reference", () => {
    const { container } = render(
      <TokenText value="${upstream.date_1.out}" labels={labels} />,
    );
    expect(container.querySelectorAll(".dz-token-chip")).toHaveLength(1);
    expect(screen.getByText("Date & time · Formatted")).toBeInTheDocument();
    expect(container.textContent).not.toContain("${");
  });

  // The regression this exists for.
  it("chips a reference embedded in text, keeping the text", () => {
    const { container } = render(
      <TokenText value="Deadline: ${upstream.date_1.out}" labels={labels} />,
    );
    expect(container.querySelectorAll(".dz-token-chip")).toHaveLength(1);
    expect(container.textContent).toContain("Deadline: ");
    expect(container.textContent).not.toContain("${");
  });

  it("chips several references in one value", () => {
    const { container } = render(
      <TokenText
        value="from ${trigger.body.a} to ${trigger.body.b}"
        labels={labels}
      />,
    );
    expect(container.querySelectorAll(".dz-token-chip")).toHaveLength(2);
    expect(container.textContent).not.toContain("${");
  });

  // A secret in a field is worth recognising at a glance on a surface you scan.
  it("marks a secret reference and shows its name, not the syntax", () => {
    const { container } = render(
      <TokenText value="Bearer ${secret.API_KEY}" labels={labels} />,
    );
    expect(container.querySelectorAll(".dz-token-chip-secret")).toHaveLength(1);
    expect(screen.getByText("API_KEY")).toBeInTheDocument();
    expect(container.textContent).not.toContain("${");
  });

  it("leaves plain text alone", () => {
    const { container } = render(
      <TokenText value="just words" labels={labels} />,
    );
    expect(container.querySelectorAll(".dz-token-chip")).toHaveLength(0);
    expect(container.textContent).toBe("just words");
  });

  // An unparseable token still shows SOMETHING rather than an empty chip —
  // the raw token is the only name we have for it, and silence would be worse.
  it("falls back to the raw token when it cannot be described", () => {
    const { container } = render(
      <TokenText value="${upstream.weird[}" labels={labels} />,
    );
    expect(container.textContent).toContain("upstream.weird");
  });
});
