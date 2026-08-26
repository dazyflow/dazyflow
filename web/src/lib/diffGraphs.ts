// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Graph, Node, Edge } from "../types";

// GraphDiff summarizes how a draft graph differs from a baseline (the
// published revision). It's execution-focused: node/edge structure and
// per-node params/module — the things that change what a run does. Purely
// cosmetic deltas (node positions, frames, editor waypoints) are ignored
// so "your draft differs from live" doesn't fire on a drag.
//
// The cosmetic set here is the mirror of core.BehaviorEqual (Go), which
// decides whether the editor prompts to publish at all — keep the two in
// lockstep. When they drift the editor contradicts itself: the toolbar
// announces unpublished changes and this view calls the draft identical.
export type NodeChange = {
  id: string;
  // Which fields changed — module swap and/or params edit. Empty for an
  // added/removed node (the whole node is the change).
  fields: (
    | "module"
    | "params"
    | "env"
    | "disabled"
    | "breakpoint"
    | "continue_on_error"
    | "timeout_seconds"
  )[];
};

export type GraphDiff = {
  addedNodes: string[];
  removedNodes: string[];
  changedNodes: NodeChange[];
  addedEdges: string[];
  removedEdges: string[];
  // metaChanged lists graph-level settings that differ (name, triggers,
  // failure_notify, timeout, visibility) — they affect behaviour or
  // routing even though they aren't nodes/edges. The catch-all "other"
  // appears when the two revisions differ somewhere this diff doesn't
  // itemize (see diffGraphs), so an empty diff always means "identical".
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
  // Not cosmetic despite being setup-time aids: a breakpoint pauses the
  // run, continue_on_error changes whether a failure fails the run, and a
  // node timeout can cancel one.
  if (!!a.breakpoint !== !!b.breakpoint) fields.push("breakpoint");
  if (!!a.continue_on_error !== !!b.continue_on_error) fields.push("continue_on_error");
  if ((a.timeout_seconds ?? 0) !== (b.timeout_seconds ?? 0)) fields.push("timeout_seconds");
  return fields;
}

// stripCosmetic clears the editor-only fields, mirroring core.stripCosmetic
// (Go). Used for the catch-all check below, so a difference this diff has no
// itemizer for is still reported rather than shown as "identical".
//
// Note `disabled` (the flow-level pause) is cleared too: it takes effect from
// HEAD the moment it's saved, so it is never something to publish.
function stripCosmetic(g: Graph): unknown {
  return {
    ...g,
    disabled: false,
    frames: undefined,
    // A step's name and position are both editor presentation — see
    // core.BehaviorEqual for why the name is here and the FLOW's name isn't.
    nodes: (g.nodes ?? []).map((n) => ({ ...n, position: undefined, label: undefined })),
    edges: (g.edges ?? []).map((e) => ({ ...e, waypoints: undefined })),
  };
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
  if ((baseline.icon ?? "") !== (draft.icon ?? "")) metaChanged.push("icon");
  if ((baseline.description ?? "") !== (draft.description ?? ""))
    metaChanged.push("description");
  if ((baseline.owner ?? "") !== (draft.owner ?? "")) metaChanged.push("owner");

  // Catch-all. Everything above is an itemizer for a field we know about,
  // and the graph grows fields faster than this list does. The server's
  // publish prompt compares whole revisions (core.BehaviorEqual), so
  // without this a new field would show up there as "you have unpublished
  // changes" and here as "your draft matches what's live" — the exact
  // contradiction this file's cosmetic set exists to avoid.
  const itemized =
    addedNodes.length > 0 ||
    removedNodes.length > 0 ||
    changedNodes.length > 0 ||
    addedEdges.length > 0 ||
    removedEdges.length > 0 ||
    metaChanged.length > 0;
  if (!itemized && stable(stripCosmetic(baseline)) !== stable(stripCosmetic(draft))) {
    metaChanged.push("other");
  }

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
