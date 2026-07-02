// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from "vitest";
import { highlightCEL } from "./celHighlight";

describe("highlightCEL", () => {
  it("classifies strings, numbers, vars, fields and operators", () => {
    const html = highlightCEL('input.total > 100 && input.name == "hi"');
    expect(html).toContain('<span class="cel-var">input</span>'); // bound var
    expect(html).toContain('<span class="cel-num">100</span>');
    expect(html).toContain('<span class="cel-str">"hi"</span>');
    expect(html).toContain('<span class="cel-op">&gt;</span>'); // operator, escaped
    expect(html).toContain('<span class="cel-op">&amp;&amp;</span>');
  });

  it("colors built-in macros and called identifiers as functions", () => {
    const html = highlightCEL("input.filter(x, size(x.id) > 0)");
    expect(html).toContain('<span class="cel-fn">filter</span>'); // macro
    expect(html).toContain('<span class="cel-fn">size</span>'); // builtin
  });

  it("colors keywords/literals", () => {
    const html = highlightCEL('"x" in input && true');
    expect(html).toContain('<span class="cel-kw">in</span>');
    expect(html).toContain('<span class="cel-kw">true</span>');
  });

  it("escapes HTML in string content so it can't inject markup", () => {
    const html = highlightCEL('"<img src=x onerror=alert(1)>"');
    expect(html).not.toContain("<img");
    expect(html).toContain("&lt;img");
  });

  it("preserves whitespace and newlines verbatim", () => {
    const html = highlightCEL("input\n  * 2");
    expect(html).toContain("\n  "); // indentation kept for overlay alignment
  });
});
