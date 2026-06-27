// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Graph, Node, Edge } from "../types";

// GraphDiff summarizes how a draft graph differs from a baseline (the
// published revision). It's execution-focused: node/edge structure and
// per-node params/module — the things that change what a run does. Purely
// cosmetic deltas (node positions, frames, editor waypoints) are ignored
// so "your draft differs from live" doesn't fire on a drag.
export type NodeChange = {
  id: string;
  // Which fields changed — module swap and/or params edit. Empty for an
  // added/removed node (the whole node is the change).
  fields: ("module" | "params" | "env" | "disabled")[];
};

export type GraphDiff = {
  addedNodes: string[];
  removedNodes: string[];
  changedNodes: NodeChange[];
  addedEdges: string[];
  removedEdges: string[];
  // metaChanged lists graph-level settings that differ (name, triggers,
  // failure_notify, timeout, visibility) — they affect behaviour or
  // routing even though they aren't nodes/edges.
  metaChanged: string[];
};

// edgeKey identifies an edge by its execution-relevant endpoints, ignoring
// waypoints (editor-only routing).
function edgeKey(e: Edge): string {
  return `${e.from}:${e.from_port}→${e.to}:${e.to_port}[${e.on_error ?? ""}]`;
}

// stableParams JSON-stringifies a params object with sorted keys so the
// comparison is order-independent (object key order isn't meaningful).
function stable(v: unknown): string {
  return JSON.stringify(v, (_k, val) => {
    if (val && typeof val === "object" && !Array.isArray(val)) {
      const sorted: Record<string, unknown> = {};
      for (const k of Object.keys(val as Record<string, unknown>).sort()) {
        sorted[k] = (val as Record<string, unknown>)[k];
      }
      return sorted;
    }
    return val;
  });
}

function nodeFieldChanges(a: Node, b: Node): NodeChange["fields"] {
  const fields: NodeChange["fields"] = [];
  if (a.module !== b.module) fields.push("module");
  if (stable(a.params ?? {}) !== stable(b.params ?? {})) fields.push("params");
  if (stable(a.env ?? {}) !== stable(b.env ?? {})) fields.push("env");
  if (!!a.disabled !== !!b.disabled) fields.push("disabled");
  return fields;
}

// diffGraphs compares a draft against a baseline (published) graph. The
// argument order is (baseline, draft): additions/removals are described
// relative to the baseline, so "addedNodes" are nodes the draft has that
// the published version doesn't.
export function diffGraphs(baseline: Graph, draft: Graph): GraphDiff {
  const baseNodes = new Map(baseline.nodes.map((n) => [n.id, n]));
  const draftNodes = new Map(draft.nodes.map((n) => [n.id, n]));

  const addedNodes: string[] = [];
  const removedNodes: string[] = [];
  const changedNodes: NodeChange[] = [];

  for (const [id, dn] of draftNodes) {
    const bn = baseNodes.get(id);
    if (!bn) {
      addedNodes.push(id);
      continue;
    }
    const fields = nodeFieldChanges(bn, dn);
    if (fields.length > 0) changedNodes.push({ id, fields });
  }
  for (const id of baseNodes.keys()) {
    if (!draftNodes.has(id)) removedNodes.push(id);
  }

  const baseEdges = new Set(baseline.edges.map(edgeKey));
  const draftEdges = new Set(draft.edges.map(edgeKey));
  const addedEdges = [...draftEdges].filter((k) => !baseEdges.has(k));
  const removedEdges = [...baseEdges].filter((k) => !draftEdges.has(k));

  const metaChanged: string[] = [];
  if ((baseline.name ?? "") !== (draft.name ?? "")) metaChanged.push("name");
  if (stable(baseline.triggers ?? []) !== stable(draft.triggers ?? []))
    metaChanged.push("triggers");
  if (stable(baseline.failure_notify ?? {}) !== stable(draft.failure_notify ?? {}))
    metaChanged.push("failure_notify");
  if ((baseline.timeout_seconds ?? 0) !== (draft.timeout_seconds ?? 0))
    metaChanged.push("timeout_seconds");
  if ((baseline.visibility ?? "org") !== (draft.visibility ?? "org"))
    metaChanged.push("visibility");

  return {
    addedNodes,
    removedNodes,
    changedNodes,
    addedEdges,
    removedEdges,
    metaChanged,
  };
}

// diffIsEmpty reports whether a diff has no execution-relevant changes.
export function diffIsEmpty(d: GraphDiff): boolean {
  return (
    d.addedNodes.length === 0 &&
    d.removedNodes.length === 0 &&
    d.changedNodes.length === 0 &&
    d.addedEdges.length === 0 &&
    d.removedEdges.length === 0 &&
    d.metaChanged.length === 0
  );
}
