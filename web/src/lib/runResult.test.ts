// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { isResultNode, previewOutput, RUN_PREVIEW_MAX } from "./runResult";
import type { Edge } from "../types";

describe("previewOutput", () => {
  it("passes text through unchanged", () => {
    expect(previewOutput({ text: { data: "Orders by region\n• North: 42" } })).toBe(
      "Orders by region\n• North: 42",
    );
  });

  it("pretty-prints structured values", () => {
    expect(previewOutput({ rows: { data: [{ region: "North" }] } })).toBe(
      '[\n  {\n    "region": "North"\n  }\n]',
    );
  });

  it("takes the first port that carries an inline value", () => {
    const out = {
      empty: { data: null },
      blank: { data: "   " },
      text: { data: "the answer" },
    };
    expect(previewOutput(out)).toBe("the answer");
  });

  // A large output lives in storage and arrives as a bare reference. The ref
  // string is an internal handle, so it's no use as a preview — better to
  // report "nothing to show inline" and let the caller fall back.
  it("ignores outputs held by reference", () => {
    expect(previewOutput({ file: { ref: "blob://abc", mime: "application/pdf" } })).toBe("");
  });

  it("returns empty for missing or empty output", () => {
    expect(previewOutput(undefined)).toBe("");
    expect(previewOutput({})).toBe("");
  });

  it("truncates past the cap with an ellipsis", () => {
    const long = "x".repeat(RUN_PREVIEW_MAX + 50);
    const got = previewOutput({ text: { data: long } });
    expect(got).toHaveLength(RUN_PREVIEW_MAX + 1);
    expect(got.endsWith("…")).toBe(true);
  });

  it("honours a caller-supplied cap", () => {
    expect(previewOutput({ text: { data: "abcdef" } }, 3)).toBe("abc…");
  });
});

describe("isResultNode", () => {
  const edges: Edge[] = [
    { from: "data", from_port: "out", to: "summary", to_port: "rows" },
  ];

  it("treats a step no edge leaves as an end of the flow", () => {
    expect(isResultNode("summary", edges)).toBe(true);
  });

  it("rejects a step that feeds another", () => {
    expect(isResultNode("data", edges)).toBe(false);
  });

  it("treats a lone step as an end of the flow", () => {
    expect(isResultNode("only", [])).toBe(true);
    expect(isResultNode("only", undefined)).toBe(true);
  });
});
