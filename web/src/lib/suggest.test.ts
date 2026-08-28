// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from "vitest";
import type { DropAdjacency, Manifest } from "../types";
import { suggestNextDrops, topDropsByUsage } from "./suggest";

function man(id: string): Manifest {
  return { id, label: id } as Manifest;
}

function adj(
  from: string,
  fromPort: string,
  to: string,
  toPort: string,
  flows: number,
): DropAdjacency {
  return { from, from_port: fromPort, to, to_port: toPort, flows, edges: flows };
}

const ADJ: DropAdjacency[] = [
  adj("http_fetch", "out", "parse_json", "in", 5),
  adj("http_fetch", "out", "shell", "in", 2),
  adj("shell", "out", "ntfy", "in", 4),
  adj("parse_json", "out", "http_fetch", "in", 1),
];

const byId = new Map(
  ["http_fetch", "parse_json", "shell", "ntfy"].map((id) => [id, man(id)]),
);
const allAllowed = new Set(byId.keys());

describe("suggestNextDrops", () => {
  it("ranks downstream modules when dragging from an output", () => {
    const out = suggestNextDrops(ADJ, "http_fetch", true, "out", allAllowed, byId);
    // Sorted by summed flows: parse_json (5) then shell (2).
    expect(out.map((m) => m.id)).toEqual(["parse_json", "shell"]);
  });

  it("ranks upstream modules when dragging from an input", () => {
    const out = suggestNextDrops(ADJ, "http_fetch", false, "in", allAllowed, byId);
    expect(out.map((m) => m.id)).toEqual(["parse_json"]);
  });

  it("keys on the dragged output port for multi-output drops", () => {
    // A router with two distinct output pins leading to different drops.
    const router: DropAdjacency[] = [
      adj("route", "matched", "ntfy", "in", 9),
      adj("route", "unmatched", "shell", "in", 9),
    ];
    const ids = new Map(["ntfy", "shell"].map((id) => [id, man(id)]));
    const all = new Set(ids.keys());
    expect(
      suggestNextDrops(router, "route", true, "matched", all, ids).map((m) => m.id),
    ).toEqual(["ntfy"]);
    expect(
      suggestNextDrops(router, "route", true, "unmatched", all, ids).map((m) => m.id),
    ).toEqual(["shell"]);
  });

  it("falls back to any-port entries when the exact port never matched", () => {
    const out = suggestNextDrops(
      ADJ,
      "http_fetch",
      true,
      "some-unwired-port",
      allAllowed,
      byId,
    );
    // No entry has that port, so it widens to all of http_fetch's outputs.
    expect(out.map((m) => m.id)).toEqual(["parse_json", "shell"]);
  });

  it("drops candidates that are not MIME-compatible (not in allowed)", () => {
    const allowed = new Set(["parse_json"]); // shell excluded
    const out = suggestNextDrops(ADJ, "http_fetch", true, "out", allowed, byId);
    expect(out.map((m) => m.id)).toEqual(["parse_json"]);
  });

  it("aggregates flows across ports, de-duplicates, and caps at the limit", () => {
    const dupAdj: DropAdjacency[] = [
      adj("a", "p1", "x", "in", 9),
      adj("a", "p2", "x", "in", 8), // same target via a different port
      adj("a", "p1", "y", "in", 7),
      adj("a", "p1", "z", "in", 6),
    ];
    const ids = new Map(["x", "y", "z"].map((id) => [id, man(id)]));
    // No exact-port match for "px", so it falls back to all ports: x scores
    // 9+8=17 (highest), then y, then z — capped at 2.
    const out = suggestNextDrops(dupAdj, "a", true, "px", new Set(ids.keys()), ids, 2);
    expect(out.map((m) => m.id)).toEqual(["x", "y"]);
  });

  it("returns [] for an unknown source module or empty adjacency", () => {
    expect(suggestNextDrops(ADJ, "nope", true, "out", allAllowed, byId)).toEqual([]);
    expect(suggestNextDrops([], "http_fetch", true, "out", allAllowed, byId)).toEqual([]);
    expect(suggestNextDrops(ADJ, "", true, "out", allAllowed, byId)).toEqual([]);
  });
});

describe("topDropsByUsage", () => {
  it("ranks by total flows across both edge endpoints", () => {
    // Totals: http_fetch 5+2+1=8, parse_json 5+1=6, shell 2+4=6, ntfy 4.
    const out = topDropsByUsage(ADJ, allAllowed, byId, 3);
    expect(out[0].id).toBe("http_fetch");
    expect(out.map((m) => m.id)).toHaveLength(3);
    expect(out.map((m) => m.id)).not.toContain("ntfy"); // 4 is lowest, off the top-3
  });

  it("returns [] for empty adjacency", () => {
    expect(topDropsByUsage([], allAllowed, byId)).toEqual([]);
  });
});
