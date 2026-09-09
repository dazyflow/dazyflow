// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Column assignment for Tidy — which column each node lands in, left to right.
//
// Two passes, and the second one is the point.
//
// EARLIEST (longest path from the roots) fixes how many columns there are and
// guarantees every edge points rightward. On its own it also puts every node
// with no incoming edge in column 0, because that is where they all start and
// only targets are ever pushed along — so a Text card wired solely into the
// fifth step got parked at the far left with a wire dragged across the canvas.
//
// LATEST then slides each node right until it is one column left of its nearest
// consumer. MIN over consumers, not max: a node feeding both step 1 and step 5
// belongs beside step 1. Since a consumer's column is always greater than this
// node's earliest column, the slide can only move a node right, never past a
// predecessor.
//
// Triggers are exempt: a trigger reads as the place the flow begins, so it
// anchors the left edge even when its only wire runs to something late.

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

export interface LayoutBox {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  /** A nailed (locked) card keeps the position it has; Tidy works around it. */
  nailed?: boolean;
}

export interface PackOptions {
  hgap?: number;
  vgap?: number;
  startX?: number;
  startY?: number;
}

/**
 * packColumns turns column indices into canvas positions, one stack per column.
 *
 * Nailed cards get no entry in the result — the caller leaves them alone. They
 * still shape the outcome twice: they keep the column their edges gave them
 * (so a chain reads left to right through them), and any column band their box
 * overlaps stacks around them instead of on top of them.
 */
export function packColumns(
  boxes: LayoutBox[],
  layer: Map<string, number>,
  opts: PackOptions = {},
): Map<string, { x: number; y: number }> {
  const { hgap = 80, vgap = 36, startX = 80, startY = 80 } = opts;
  const pos = new Map<string, { x: number; y: number }>();
  const nailed = boxes.filter((b) => b.nailed).sort((a, b) => a.y - b.y);

  const cols = new Map<number, LayoutBox[]>();
  for (const b of boxes) {
    if (b.nailed) continue;
    const c = layer.get(b.id) ?? 0;
    const arr = cols.get(c);
    if (arr) arr.push(b);
    else cols.set(c, [b]);
  }
  const sortedCols = [...cols.keys()].sort((a, b) => a - b);
  let maxColH = 0;
  const colH = new Map<number, number>();
  for (const c of sortedCols) {
    const arr = cols.get(c)!;
    arr.sort((p, q) => p.y - q.y);
    const h = arr.reduce((s, b) => s + b.h, 0) + vgap * (arr.length - 1);
    colH.set(c, h);
    if (h > maxColH) maxColH = h;
  }

  let x = startX;
  for (const c of sortedCols) {
    const arr = cols.get(c)!;
    const colW = Math.max(...arr.map((b) => b.w));
    const inBand = nailed.filter((n) => n.x < x + colW && n.x + n.w > x);
    let y = startY + (maxColH - (colH.get(c) ?? 0)) / 2;
    for (const b of arr) {
      // Sorted by y and only ever pushed down, so one pass per nailed card is
      // enough to clear all of them.
      for (const n of inBand) {
        if (n.y < y + b.h && n.y + n.h > y) y = n.y + n.h + vgap;
      }
      pos.set(b.id, { x: x + (colW - b.w) / 2, y });
      y += b.h + vgap;
    }
    x += colW + hgap;
  }
  return pos;
}
