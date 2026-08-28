// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { DropAdjacency, Manifest } from "../types";

// rank turns a module-id → score map into ranked manifests: highest score
// first, keeping only ids that are `allowed` (the MIME-compatible set) and
// known in `byId`, de-duplicated and capped at `limit`. Ties keep Map
// insertion order, which the callers seed from the API's flow-sorted list.
function rank(
  scored: Map<string, number>,
  allowed: Set<string>,
  byId: Map<string, Manifest>,
  limit: number,
): Manifest[] {
  const out: Manifest[] = [];
  const ordered = [...scored.entries()].sort((a, b) => b[1] - a[1]);
  for (const [id] of ordered) {
    if (!allowed.has(id)) continue;
    const m = byId.get(id);
    if (!m) continue;
    out.push(m);
    if (out.length >= limit) break;
  }
  return out;
}

// suggestNextDrops ranks the drops to surface in the editor's "Suggested"
// group when the user drags off a port.
//
// `fromOutput` reflects which handle was grabbed: an output (source) handle
// ranks DOWNSTREAM modules (entries whose `from` is the dragged module); an
// input (target) handle ranks UPSTREAM ones (`to` is the dragged module).
// `srcPort` is the dragged port id: entries whose port on the dragged side
// matches it are preferred, so a drop with several distinct outputs (a
// router's matched/unmatched) suggests the right next step per pin. If no
// entry matches the exact port, it falls back to any-port entries for the
// module — so a single-output drop, or a port that's simply never been
// wired before, still gets module-level suggestions. Candidates are
// intersected with `allowed` (MIME-compatible) so a suggestion can always
// actually wire. Returns [] when nothing qualifies.
export function suggestNextDrops(
  adjacency: DropAdjacency[],
  srcModule: string,
  fromOutput: boolean,
  srcPort: string,
  allowed: Set<string>,
  byId: Map<string, Manifest>,
  limit = 5,
): Manifest[] {
  if (!srcModule) return [];
  // Sum flows per candidate module. portMatch=true restricts to the dragged
  // port; we try that first and only widen if it yields nothing.
  const tally = (portMatch: boolean): Map<string, number> => {
    const m = new Map<string, number>();
    for (const a of adjacency) {
      if (fromOutput ? a.from !== srcModule : a.to !== srcModule) continue;
      const port = fromOutput ? a.from_port : a.to_port;
      if (portMatch && port !== srcPort) continue;
      const id = fromOutput ? a.to : a.from;
      m.set(id, (m.get(id) ?? 0) + a.flows);
    }
    return m;
  };
  let scored = tally(true);
  if (scored.size === 0) scored = tally(false);
  return rank(scored, allowed, byId, limit);
}

// topDropsByUsage ranks the most-wired drops overall — the fallback
// "Suggested" group for the Cmd/Ctrl+K palette, where there's no source
// port to key off. A module's score is the total flows of every edge it
// participates in (either end), so frequently-used connectors float up.
export function topDropsByUsage(
  adjacency: DropAdjacency[],
  allowed: Set<string>,
  byId: Map<string, Manifest>,
  limit = 5,
): Manifest[] {
  const m = new Map<string, number>();
  for (const a of adjacency) {
    m.set(a.from, (m.get(a.from) ?? 0) + a.flows);
    m.set(a.to, (m.get(a.to) ?? 0) + a.flows);
  }
  return rank(m, allowed, byId, limit);
}
