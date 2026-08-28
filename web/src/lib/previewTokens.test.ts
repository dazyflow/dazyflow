// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { fillTokensForPreview, previewTokenSpan } from "./previewTokens";

const LABELS = { "gmail_1.out": "Gmail · Matching emails" };

describe("fillTokensForPreview", () => {
  it("stands in for a token embedded in a subject line", () => {
    // The reported bug: the preview's subject showed the wire format.
    expect(
      fillTokensForPreview("Re: ${upstream.gmail_1.out[0].subject}", LABELS),
    ).toBe("Re: Gmail · Matching emails → first → subject");
  });

  it("names a secret by its own name", () => {
    expect(fillTokensForPreview("Key: ${secret.API_KEY}", LABELS)).toBe(
      "Key: API_KEY",
    );
  });

  it("leaves a value with no tokens untouched", () => {
    expect(fillTokensForPreview("Your weekly digest", LABELS)).toBe(
      "Your weekly digest",
    );
  });

  it("keeps an unparseable token as-is rather than dropping it", () => {
    // Honest fallback: a token we can't word is better shown than swallowed.
    const raw = "${upstream.weird[[}";
    expect(fillTokensForPreview(raw, LABELS)).toBe(raw);
  });

  it("substitutes every occurrence, not just the first", () => {
    expect(
      fillTokensForPreview("${item.name} <${item.email}>", LABELS),
    ).toBe("Each row → name <Each row → email>");
  });

  it("wraps and escapes substitutions for an HTML body", () => {
    const out = fillTokensForPreview(
      "<p>Hi ${item.name}</p>",
      LABELS,
      previewTokenSpan,
    );
    expect(out).toContain("<p>Hi <span");
    expect(out).toContain("Each row → name</span>");
    // The surrounding body markup is the author's own and stays raw.
    expect(out).toContain("</p>");
  });

  it("escapes markup carried in by a step label", () => {
    const out = fillTokensForPreview(
      "${upstream.n1.out}",
      { "n1.out": "<script>x</script>" },
      previewTokenSpan,
    );
    expect(out).not.toContain("<script>");
    expect(out).toContain("&lt;script&gt;");
  });
});
