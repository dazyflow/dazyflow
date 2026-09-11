// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import {
  isResultNode,
  pickResultNode,
  previewOutput,
  resultFilename,
  resultView,
  RUN_PREVIEW_MAX,
} from "./runResult";
import type { Edge, JobRecord, JobStatus, Ref } from "../types";

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

  // Go marshals a result map in key order, so "failed" and "logs" arrive ahead
  // of "out" on spelling alone. The Code step's preview showed its log — or an
  // empty failed list — where its answer belonged.
  it("passes over the explanatory ports to find the answer", () => {
    const out = {
      failed: { data: [] },
      logs: { data: [{ index: 0, line: 1, level: "log", message: "hi" }] },
      out: { data: { total: 12 } },
    };
    expect(previewOutput(out)).toBe('{\n  "total": 12\n}');
  });

  it("still shows an explanatory port when it is all there is", () => {
    expect(previewOutput({ stderr: { data: "command not found" } })).toBe(
      "command not found",
    );
  });

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

describe("resultView", () => {
  it("classifies a rows value as a table", () => {
    const view = resultView({
      rows: {
        data: [{ region: "North", n: 42 }],
        headers: ["region", "n"],
      },
    });
    expect(view).toEqual({
      kind: "rows",
      port: "rows",
      headers: ["region", "n"],
      rows: [{ region: "North", n: 42 }],
    });
  });

  // The value's own header order is the producing step's order, which is the
  // order the table should default to — not whatever Object.keys hands back.
  it("prefers the declared column order", () => {
    const view = resultView({
      rows: { data: [{ b: 2, a: 1 }], headers: ["a", "b"] },
    });
    expect(view.kind === "rows" && view.headers).toEqual(["a", "b"]);
  });

  it("appends columns the rows carry but did not declare", () => {
    const view = resultView({
      rows: { data: [{ a: 1, extra: 9 }], headers: ["a"] },
    });
    expect(view.kind === "rows" && view.headers).toEqual(["a", "extra"]);
  });

  it("derives columns when the value declares none", () => {
    const view = resultView({ rows: { data: [{ a: 1 }, { b: 2 }] } });
    expect(view.kind === "rows" && view.headers).toEqual(["a", "b"]);
  });

  // An array of scalars, or a mixed one, would need invented columns.
  it("keeps a non-object array as text", () => {
    expect(resultView({ out: { data: [1, 2, 3] } }).kind).toBe("text");
    expect(resultView({ out: { data: [{ a: 1 }, "x"] } }).kind).toBe("text");
  });

  it("keeps an empty array as text rather than an empty table", () => {
    expect(resultView({ rows: { data: [] } }).kind).toBe("text");
  });

  it("passes text through untruncated", () => {
    const long = "x".repeat(RUN_PREVIEW_MAX * 3);
    const view = resultView({ text: { data: long } });
    expect(view.kind === "text" && view.text).toBe(long);
  });

  it("pretty-prints a single object", () => {
    const view = resultView({ out: { data: { region: "North" } } });
    expect(view.kind === "text" && view.text).toBe('{\n  "region": "North"\n}');
  });

  // Same port-picking rule as previewOutput, so the panel and the editor's
  // post-run banner never name different steps as "the result".
  it("takes the first port carrying an inline value", () => {
    const view = resultView({
      empty: { data: null },
      blank: { data: "  " },
      text: { data: "the answer" },
    });
    expect(view.kind === "text" && view.port).toBe("text");
  });

  it("passes over the explanatory ports to find the answer", () => {
    const view = resultView({
      logs: { data: [{ index: 0, message: "hi" }] },
      out: { data: "the answer" },
    });
    expect(view.kind === "text" && view.port).toBe("out");
  });

  it("reports none for a by-reference or absent output", () => {
    expect(resultView({ file: { ref: "reports/x.pdf" } }).kind).toBe("none");
    expect(resultView(undefined).kind).toBe("none");
    expect(resultView({}).kind).toBe("none");
  });
});

describe("resultFilename", () => {
  const rows = resultView({ rows: { data: [{ a: 1 }] } });

  it("names rows as CSV under the flow's name", () => {
    expect(resultFilename(rows, "Weekly report")).toBe("Weekly-report.csv");
  });

  it("uses .json only when the text really is JSON", () => {
    expect(resultFilename(resultView({ o: { data: { a: 1 } } }), "f")).toBe("f.json");
    expect(resultFilename(resultView({ o: { data: "plain" } }), "f")).toBe("f.txt");
  });

  it("honours a JSON mime over the shape sniff", () => {
    const view = resultView({
      o: { data: "not-braces", mime: "application/json" },
    });
    expect(resultFilename(view, "f")).toBe("f.json");
  });

  it("falls back when the flow name has no usable characters", () => {
    expect(resultFilename(rows, "///")).toBe("result.csv");
    expect(resultFilename(rows, "")).toBe("result.csv");
  });
});

describe("pickResultNode", () => {
  const node = (
    nodeID: string,
    output: Record<string, Ref>,
    status: JobStatus = "succeeded",
  ): JobRecord => ({
    ID: "j-" + nodeID,
    Kind: "node",
    GraphRunID: "r",
    GraphID: "g",
    NodeID: nodeID,
    Status: status,
    Result: { output },
  });

  it("prefers a step at the end of the flow over the plumbing", () => {
    const nodes = [
      node("fetch", { out: { data: "raw" } }),
      node("summary", { text: { data: "42 orders" } }),
    ];
    const edges: Edge[] = [
      { from: "fetch", from_port: "out", to: "summary", to_port: "in" },
    ];
    expect(pickResultNode(nodes, edges, "succeeded")?.NodeID).toBe("summary");
  });

  it("shows no result when the end of the flow wrote a file", () => {
    const nodes = [
      node("literal", { out: { data: '[{"a":1}]' } }),
      node("csv", { out: { data: "a\n1" } }),
      node("save", { out: { ref: "reports/x.csv", mime: "text/csv" } }),
    ];
    const edges: Edge[] = [
      { from: "literal", from_port: "out", to: "csv", to_port: "rows" },
      { from: "csv", from_port: "out", to: "save", to_port: "in" },
    ];
    expect(pickResultNode(nodes, edges, "succeeded")).toBeNull();
  });

  it("still shows a value when another end step produced one", () => {
    const nodes = [
      node("rows", { out: { data: '[{"a":1}]' } }),
      node("save", { out: { ref: "reports/x.csv" } }),
      node("summary", { text: { data: "1 row saved" } }),
    ];
    const edges: Edge[] = [
      { from: "rows", from_port: "out", to: "save", to_port: "in" },
      { from: "rows", from_port: "out", to: "summary", to_port: "in" },
    ];
    expect(pickResultNode(nodes, edges, "succeeded")?.NodeID).toBe("summary");
  });

  it("falls back to the last valued step without a graph", () => {
    const nodes = [
      node("a", { out: { data: "first" } }),
      node("b", { out: { data: "second" } }),
    ];
    expect(pickResultNode(nodes, undefined, "succeeded")?.NodeID).toBe("b");
  });

  it("ignores steps that did not succeed", () => {
    const nodes = [
      node("ok", { out: { data: "kept" } }),
      node("bad", { out: { data: "ignored" } }, "failed"),
    ];
    expect(pickResultNode(nodes, undefined, "succeeded")?.NodeID).toBe("ok");
  });

  it("shows nothing for a run that did not succeed", () => {
    const nodes = [node("a", { out: { data: "x" } })];
    expect(pickResultNode(nodes, undefined, "failed")).toBeNull();
    expect(pickResultNode(nodes, undefined, "running")).toBeNull();
  });

  it("shows nothing when no step produced anything inline", () => {
    expect(pickResultNode([node("a", {})], undefined, "succeeded")).toBeNull();
    expect(pickResultNode([], undefined, "succeeded")).toBeNull();
  });
});
