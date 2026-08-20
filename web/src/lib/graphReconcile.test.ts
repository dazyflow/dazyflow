// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { reconcileByID, samePosition, sameData } from "./graphReconcile";

interface FakeNode {
  id: string;
  position: { x: number; y: number };
  data: { moduleID: string };
}
interface Target {
  id: string;
  module: string;
  position: { x: number; y: number };
}

function node(id: string, module: string, x = 0, y = 0): FakeNode {
  return { id, position: { x, y }, data: { moduleID: module } };
}
function target(id: string, module: string, x = 0, y = 0): Target {
  return { id, module, position: { x, y } };
}

const opts = {
  idOfExisting: (n: FakeNode) => n.id,
  idOfTarget: (t: Target) => t.id,
  isUnchanged: (n: FakeNode, t: Target) =>
    n.data.moduleID === t.module && samePosition(n.position, t.position),
  build: (t: Target): FakeNode => node(t.id, t.module, t.position.x, t.position.y),
};

describe("reconcileByID", () => {
  it("returns the original array by reference when nothing changed", () => {
    // The strongest form of the optimisation: a no-op apply must not even
    // re-render the canvas.
    const existing = [node("a", "http_request"), node("b", "slack_send_message")];
    const out = reconcileByID(existing, [target("a", "http_request"), target("b", "slack_send_message")], opts);
    expect(out.items).toBe(existing);
    expect(out.built).toBe(0);
    expect(out.reused).toBe(2);
  });

  it("rebuilds only the item that changed", () => {
    // This is the property that makes undo snappy: dragging one node of fifty
    // and undoing it must touch one card.
    const a = node("a", "http_request");
    const b = node("b", "slack_send_message");
    const c = node("c", "delay");
    const out = reconcileByID(
      [a, b, c],
      [target("a", "http_request"), target("b", "slack_send_message", 400, 80), target("c", "delay")],
      opts,
    );
    expect(out.reused).toBe(2);
    expect(out.built).toBe(1);
    expect(out.items[0]).toBe(a); // reference preserved
    expect(out.items[2]).toBe(c);
    expect(out.items[1]).not.toBe(b);
    expect(out.items[1].position).toEqual({ x: 400, y: 80 });
  });

  it("keeps identity for surviving items when one is deleted", () => {
    const a = node("a", "http_request");
    const b = node("b", "slack_send_message");
    const out = reconcileByID([a, b], [target("a", "http_request")], opts);
    expect(out.items).toHaveLength(1);
    expect(out.items[0]).toBe(a);
    expect(out.built).toBe(0);
  });

  it("keeps identity for existing items when one is added", () => {
    const a = node("a", "http_request");
    const out = reconcileByID([a], [target("a", "http_request"), target("z", "delay", 240, 0)], opts);
    expect(out.items[0]).toBe(a);
    expect(out.reused).toBe(1);
    expect(out.built).toBe(1);
  });

  it("follows target order, since React Flow renders in array order", () => {
    const a = node("a", "http_request");
    const b = node("b", "slack_send_message");
    const out = reconcileByID([a, b], [target("b", "slack_send_message"), target("a", "http_request")], opts);
    expect(out.items.map((n) => n.id)).toEqual(["b", "a"]);
    // Both objects are still reused even though the order changed.
    expect(out.reused).toBe(2);
    expect(out.items[0]).toBe(b);
    expect(out.items[1]).toBe(a);
    // A reorder is a real change, so the array itself must be new.
    expect(out.items).not.toBe([a, b]);
  });

  it("rebuilds when an id is reused for a different module", () => {
    // Same id, different drop — reusing the object would show the old card.
    const a = node("a", "http_request");
    const out = reconcileByID([a], [target("a", "delay")], opts);
    expect(out.items[0]).not.toBe(a);
    expect(out.items[0].data.moduleID).toBe("delay");
  });

  it("hands the previous object to build, so it can carry state forward", () => {
    const a = node("a", "http_request");
    const seen: (FakeNode | undefined)[] = [];
    reconcileByID([a], [target("a", "http_request", 99, 99)], {
      ...opts,
      build: (t, prev) => {
        seen.push(prev);
        return node(t.id, t.module, t.position.x, t.position.y);
      },
    });
    expect(seen).toEqual([a]);
  });

  it("handles both lists being empty", () => {
    const out = reconcileByID<FakeNode, Target>([], [], opts);
    expect(out.items).toEqual([]);
    expect(out.built).toBe(0);
  });

  it("builds everything when starting from empty", () => {
    const out = reconcileByID([], [target("a", "http_request"), target("b", "delay")], opts);
    expect(out.built).toBe(2);
    expect(out.reused).toBe(0);
  });
});

describe("samePosition", () => {
  it("ignores sub-pixel drift from JSON round-tripping", () => {
    // React Flow writes fractional positions; treating a value the user cannot
    // see as a move would rebuild the card on every apply.
    expect(samePosition({ x: 10, y: 20 }, { x: 10.2, y: 19.9 })).toBe(true);
  });
  it("reports a real move", () => {
    expect(samePosition({ x: 10, y: 20 }, { x: 14, y: 20 })).toBe(false);
  });
  it("handles undefined on either side", () => {
    expect(samePosition(undefined, undefined)).toBe(true);
    expect(samePosition({ x: 0, y: 0 }, undefined)).toBe(false);
    expect(samePosition(undefined, { x: 0, y: 0 })).toBe(false);
  });
});

describe("sameData", () => {
  it("compares structurally", () => {
    expect(sameData({ waypoints: [{ x: 1, y: 2 }] }, { waypoints: [{ x: 1, y: 2 }] })).toBe(true);
    expect(sameData({ waypoints: [] }, { waypoints: [{ x: 1, y: 2 }] })).toBe(false);
  });
  it("treats undefined and null alike", () => {
    expect(sameData(undefined, null)).toBe(true);
  });
});
