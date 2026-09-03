// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { findStrayEdges, type NodePorts } from "./strayEdges";
import type { Manifest } from "../types";

function manifest(
  id: string,
  inputs: string[],
  outputs: string[],
  extra: Partial<Manifest> = {},
): Manifest {
  return {
    id,
    version: "1.0",
    label: id,
    inputs: inputs.map((port) => ({ port })),
    outputs: outputs.map((port) => ({ port })),
    ...extra,
  } as Manifest;
}

const edge = (
  source: string,
  sourceHandle: string | null,
  target: string,
  targetHandle: string | null,
) => ({ source, sourceHandle, target, targetHandle });

// The shape of the reported bug: an edge into a value source, which declares
// no inputs at all.
const HTTP = manifest("http_download", ["pass"], ["pass", "out"]);
const TEXT = manifest("text", [], ["out"]);

describe("findStrayEdges", () => {
  it("drops an edge into a step that has no such input", () => {
    const nodes = new Map<string, NodePorts>([
      ["http_1", { manifest: HTTP }],
      ["text_1", { manifest: TEXT }],
    ]);
    const e = edge("http_1", "out", "text_1", "in");
    const stray = findStrayEdges([e], nodes);
    expect(stray).toHaveLength(1);
    expect(stray[0].edge).toBe(e);
    expect(stray[0].reason).toEqual({
      end: "to",
      nodeID: "text_1",
      port: "in",
      module: "text",
    });
  });

  it("drops an edge off a port the source has not got", () => {
    const nodes = new Map<string, NodePorts>([
      ["http_1", { manifest: HTTP }],
      ["http_2", { manifest: HTTP }],
    ]);
    const stray = findStrayEdges([edge("http_1", "nope", "http_2", "pass")], nodes);
    expect(stray).toHaveLength(1);
    expect(stray[0].reason).toMatchObject({
      end: "from",
      nodeID: "http_1",
      port: "nope",
    });
  });

  it("reports nothing for a graph whose wiring is sound", () => {
    const nodes = new Map<string, NodePorts>([
      ["http_1", { manifest: HTTP }],
      ["http_2", { manifest: HTTP }],
    ]);
    expect(findStrayEdges([edge("http_1", "out", "http_2", "pass")], nodes)).toEqual([]);
  });

  it("singles out only the bad edge", () => {
    const nodes = new Map<string, NodePorts>([
      ["http_1", { manifest: HTTP }],
      ["http_2", { manifest: HTTP }],
      ["text_1", { manifest: TEXT }],
    ]);
    const bad = edge("http_1", "out", "text_1", "in");
    const good = edge("http_1", "out", "http_2", "pass");
    const stray = findStrayEdges([bad, good], nodes);
    expect(stray.map((s) => s.edge)).toEqual([bad]);
  });

  // A null handle is what React Flow stores for an unnamed pin, and the
  // editor reads it as "out"/"in" — which is how the daemon comes to report a
  // missing port called "in".
  it("resolves a null handle the way the editor does", () => {
    const nodes = new Map<string, NodePorts>([
      ["http_1", { manifest: HTTP }],
      ["text_1", { manifest: TEXT }],
    ]);
    const stray = findStrayEdges([edge("http_1", null, "text_1", null)], nodes);
    expect(stray).toHaveLength(1);
    expect(stray[0].reason).toMatchObject({ port: "in", end: "to" });
  });

  // Every one of these mirrors a skip in core.Validate. Judging them would
  // delete an edge the daemon accepts — a far worse failure than the one this
  // is fixing.
  it("does not judge a step whose manifest this client lacks", () => {
    const nodes = new Map<string, NodePorts>([
      ["runner_1", {}],
      ["text_1", { manifest: TEXT }],
    ]);
    expect(findStrayEdges([edge("runner_1", "out", "text_1", "in")], nodes)).toEqual([]);
    // ... in either direction.
    const nodes2 = new Map<string, NodePorts>([
      ["http_1", { manifest: HTTP }],
      ["mcp_1", {}],
    ]);
    expect(findStrayEdges([edge("http_1", "out", "mcp_1", "whatever")], nodes2)).toEqual([]);
  });

  it("does not judge an edge touching a disabled step", () => {
    const nodes = new Map<string, NodePorts>([
      ["http_1", { manifest: HTTP }],
      ["text_1", { manifest: TEXT, disabled: true }],
    ]);
    expect(findStrayEdges([edge("http_1", "out", "text_1", "in")], nodes)).toEqual([]);
  });

  it("does not judge a step whose ports come from its params", () => {
    const sub = manifest("subgraph", [], [], { dynamic_ports: true });
    const nodes = new Map<string, NodePorts>([
      ["http_1", { manifest: HTTP }],
      ["subgraph_1", { manifest: sub }],
    ]);
    expect(findStrayEdges([edge("http_1", "out", "subgraph_1", "mapped")], nodes)).toEqual([]);
  });

  // An edge naming a node that isn't in the graph is a different defect, and
  // deleting the edge would not explain it.
  it("leaves an edge to an unknown node alone", () => {
    const nodes = new Map<string, NodePorts>([["http_1", { manifest: HTTP }]]);
    expect(findStrayEdges([edge("http_1", "out", "ghost", "in")], nodes)).toEqual([]);
  });

  it("handles an empty edge list", () => {
    expect(findStrayEdges([], new Map())).toEqual([]);
  });

  // The passthrough pin is a real declared port, so wiring to it is fine.
  it("accepts the passthrough pin", () => {
    const nodes = new Map<string, NodePorts>([
      ["http_1", { manifest: HTTP }],
      ["http_2", { manifest: HTTP }],
    ]);
    expect(findStrayEdges([edge("http_1", "pass", "http_2", "pass")], nodes)).toEqual([]);
  });
});
