// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tidy used longest-path-from-roots alone, which starts every node at column 0
// and only ever pushes TARGETS right. Anything with no incoming edge therefore
// stayed at the far left however late its consumer was — so a Text card wired
// solely into the fifth step sat in column 0 with a wire dragged across the
// whole canvas, and the layout looked wrong in exactly the case a person
// notices.

import { describe, expect, it } from "vitest";
import { layerNodes, packColumns } from "./autoLayout";

const e = (source: string, target: string) => ({ source, target });

const chain = ["a", "b", "c", "d", "eStep"];
const chainEdges = [e("a", "b"), e("b", "c"), e("c", "d"), e("d", "eStep")];

describe("layerNodes", () => {
  it("puts a card next to the step it feeds, not at the far left", () => {
    const layer = layerNodes(
      [...chain, "text"],
      [...chainEdges, e("text", "eStep")],
    );
    expect(layer.get("eStep")).toBe(4);
    expect(layer.get("text")).toBe(3);
  });

  it("keeps every edge pointing rightward", () => {
    const ids = [...chain, "text"];
    const edges = [...chainEdges, e("text", "eStep")];
    const layer = layerNodes(ids, edges);
    for (const { source, target } of edges) {
      expect(layer.get(source)!).toBeLessThan(layer.get(target)!);
    }
  });

  it("places a card by its NEAREST consumer, not its furthest", () => {
    // Feeding both step b and step eStep, it has to sit left of b — taking the
    // max would drag it past a node it must precede.
    const layer = layerNodes(
      [...chain, "shared"],
      [...chainEdges, e("shared", "b"), e("shared", "eStep")],
    );
    expect(layer.get("shared")).toBe(0);
    expect(layer.get("shared")!).toBeLessThan(layer.get("b")!);
  });

  it("leaves an already-tidy chain exactly where it was", () => {
    const layer = layerNodes(chain, chainEdges);
    expect([...chain].map((id) => layer.get(id))).toEqual([0, 1, 2, 3, 4]);
  });

  it("anchors a trigger at the left even when its only wire runs late", () => {
    // The entry point reads as where the flow begins; shortening that one edge
    // would cost more than it buys.
    const layer = layerNodes(
      [...chain, "hook"],
      [...chainEdges, e("hook", "eStep")],
      (id) => id === "hook",
    );
    expect(layer.get("hook")).toBe(0);
  });

  it("still slides non-trigger cards when a trigger is present", () => {
    const layer = layerNodes(
      [...chain, "hook", "text"],
      [...chainEdges, e("hook", "a"), e("text", "eStep")],
      (id) => id === "hook",
    );
    expect(layer.get("hook")).toBe(0);
    expect(layer.get("text")).toBe(4); // the chain shifts right behind the hook
  });

  it("terminates on a cycle instead of spinning", () => {
    // Both passes are bounded by |V| for exactly this.
    const layer = layerNodes(
      ["x", "y", "z"],
      [e("x", "y"), e("y", "z"), e("z", "x")],
    );
    expect(layer.size).toBe(3);
    for (const id of ["x", "y", "z"]) {
      expect(Number.isFinite(layer.get(id)!)).toBe(true);
    }
  });

  it("handles an empty graph and a lone node", () => {
    expect(layerNodes([], []).size).toBe(0);
    expect(layerNodes(["only"], []).get("only")).toBe(0);
  });

  it("leaves a disconnected node at the left rather than inventing a column", () => {
    const layer = layerNodes([...chain, "loose"], chainEdges);
    expect(layer.get("loose")).toBe(0);
  });

  it("pulls a chain of loose cards along together", () => {
    // text2 -> text1 -> eStep. Both should end up beside the step, in order.
    const layer = layerNodes(
      [...chain, "text1", "text2"],
      [...chainEdges, e("text2", "text1"), e("text1", "eStep")],
    );
    expect(layer.get("text1")).toBe(3);
    expect(layer.get("text2")).toBe(2);
  });
});

// Nailed (locked) steps promise on the card that they will not move. Tidy
// rearranges everything at once, which is precisely where that promise gets
// broken without a guard.
describe("packColumns", () => {
  const box = (id: string, x: number, y: number, nailed = false) => ({
    id,
    x,
    y,
    w: 200,
    h: 100,
    nailed,
  });
  const layerOf = (...cols: string[][]) => {
    const m = new Map<string, number>();
    cols.forEach((ids, i) => ids.forEach((id) => m.set(id, i)));
    return m;
  };

  it("returns no position for a nailed card", () => {
    const pos = packColumns(
      [box("a", 0, 0), box("b", 999, 999, true), box("c", 0, 0)],
      layerOf(["a"], ["b"], ["c"]),
    );
    expect(pos.has("b")).toBe(false);
    expect(pos.has("a")).toBe(true);
    expect(pos.has("c")).toBe(true);
  });

  it("lays out the rest normally when nothing is nailed", () => {
    const pos = packColumns([box("a", 0, 0), box("b", 0, 0)], layerOf(["a"], ["b"]));
    expect(pos.get("a")).toEqual({ x: 80, y: 80 });
    expect(pos.get("b")).toEqual({ x: 360, y: 80 }); // 80 + 200 + 80 hgap
  });

  it("stacks around a nailed card standing in the column band", () => {
    // The nail sits where column 0 starts, so the two loose cards go below it
    // rather than on top of it.
    const pos = packColumns(
      [box("nail", 80, 80, true), box("a", 0, 0), box("b", 0, 10)],
      layerOf(["a", "b"]),
    );
    expect(pos.get("a")!.y).toBeGreaterThanOrEqual(80 + 100);
    expect(pos.get("b")!.y).toBeGreaterThanOrEqual(pos.get("a")!.y + 100);
  });

  it("ignores a nailed card parked outside the column band", () => {
    const pos = packColumns(
      [box("nail", 5000, 80, true), box("a", 0, 0)],
      layerOf(["a"]),
    );
    expect(pos.get("a")).toEqual({ x: 80, y: 80 });
  });

  it("clears a stack of nailed cards in one pass", () => {
    const pos = packColumns(
      [box("n1", 80, 80, true), box("n2", 80, 216, true), box("a", 0, 0)],
      layerOf(["a"]),
    );
    expect(pos.get("a")!.y).toBeGreaterThanOrEqual(216 + 100);
  });

  it("leaves every card alone when they are all nailed", () => {
    const pos = packColumns(
      [box("a", 1, 2, true), box("b", 3, 4, true)],
      layerOf(["a"], ["b"]),
    );
    expect(pos.size).toBe(0);
  });
});
