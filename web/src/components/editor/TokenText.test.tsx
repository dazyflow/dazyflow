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
import { hasToken, tokenizeValue } from "./nodeCardShared";

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

// Every display surface asks hasToken() first and tokenizes only if it says
// yes. That ORDER is the whole test: the helpers shared one global regex, and
// a global regex carries lastIndex — test() left it at the end of the match it
// had just found, so the tokenizer then started past the only token in the
// value and found none. The chip container rendered around nothing and the raw
// ${…} showed through.
//
// Every test above passes against that bug, because each calls the tokenizer
// on a fresh value with lastIndex still at zero. Only the pair reproduces it.
describe("after hasToken has run", () => {
  it("still tokenizes the same value", () => {
    const value = "${trigger.body.email}";
    expect(hasToken(value)).toBe(true);
    const segs = tokenizeValue(value);
    expect(segs.filter((s) => s.kind === "token")).toHaveLength(1);
  });

  it("still chips, which is what the card actually does", () => {
    const value = "${upstream.date_1.out}";
    expect(hasToken(value)).toBe(true);
    const { container } = render(<TokenText value={value} labels={labels} />);
    expect(container.querySelectorAll(".dz-token-chip")).toHaveLength(1);
    expect(container.textContent).not.toContain("${");
  });

  it("survives hasToken being asked about several values first", () => {
    // A card asks per field, so by the time the third one tokenizes the shared
    // state had been advanced repeatedly.
    for (const v of ["${a.b}", "${c.d}", "prose ${e.f} more"]) hasToken(v);
    expect(tokenizeValue("${trigger.body.id}").filter((s) => s.kind === "token")).toHaveLength(1);
  });

  it("reports the same answer when asked twice", () => {
    // test() on a global regex alternates true/false on a repeated call —
    // hasToken must be a pure question about the value.
    const value = "${item.email}";
    expect(hasToken(value)).toBe(true);
    expect(hasToken(value)).toBe(true);
  });
});
