// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Column assignment for Tidy — which column each node lands in, left to right.
//
// Two passes, and the second one is the point.
//
// EARLIEST (longest path from the roots) fixes how many columns there are and
// guarantees every edge points rightward: a node sits at least one column right
// of everything feeding it. On its own it also puts every node with no incoming
// edge in column 0 — because that is where they all start and only targets are
// ever pushed along. A Text card wired solely into the fifth step is not a
// root of anything a reader cares about, but it looked like one, so Tidy parked
// it at the far left with a wire dragged across the whole canvas. That is the
// bug this file exists to fix.
//
// LATEST then slides each node right until it is one column left of its
// nearest consumer. MIN over consumers, not max: a node feeding both step 1 and
// step 5 belongs beside step 1, and taking the max would drag it past a node it
// has to precede. Since a consumer's column is always at least one greater than
// this node's earliest column, the slide can only move a node right, never past
// a predecessor — the first pass's ordering survives untouched.
//
// Triggers are exempt. A trigger is the graph's entry point and reads as the
// place the flow begins, so it anchors the left edge even when its only wire
// runs to something late. Shortening that one edge would cost more than it buys.

export interface LayoutEdge {
  source: string;
  target: string;
}

/**
 * layerNodes assigns each id a column index.
 *
 * `isTrigger` marks entry-point nodes, which stay in the column their
 * dependencies put them in rather than sliding right toward their consumers.
 */
export function layerNodes(
  ids: string[],
  edges: LayoutEdge[],
  isTrigger: (id: string) => boolean = () => false,
): Map<string, number> {
  const layer = new Map<string, number>(ids.map((id) => [id, 0]));
  if (ids.length === 0) return layer;

  // Pass 1 — earliest. |V| relaxation passes settle any DAG's longest paths;
  // the cap is also what stops a cycle diverging instead of looping forever.
  for (let pass = 0; pass < ids.length; pass++) {
    let changed = false;
    for (const e of edges) {
      const want = (layer.get(e.source) ?? 0) + 1;
      if (want > (layer.get(e.target) ?? 0)) {
        layer.set(e.target, want);
        changed = true;
      }
    }
    if (!changed) break;
  }

  // Pass 2 — latest. Same bounded-relaxation shape, and monotonically
  // increasing, so it converges for the same reason and a cycle cannot spin.
  const consumers = new Map<string, string[]>();
  for (const e of edges) {
    const list = consumers.get(e.source);
    if (list) list.push(e.target);
    else consumers.set(e.source, [e.target]);
  }
  for (let pass = 0; pass < ids.length; pass++) {
    let changed = false;
    for (const id of ids) {
      if (isTrigger(id)) continue;
      const outs = consumers.get(id);
      if (!outs || outs.length === 0) continue; // a sink has nothing to hug
      let nearest = Infinity;
      for (const t of outs) nearest = Math.min(nearest, layer.get(t) ?? 0);
      const want = nearest - 1;
      if (want > (layer.get(id) ?? 0)) {
        layer.set(id, want);
        changed = true;
      }
    }
    if (!changed) break;
  }
  return layer;
}
