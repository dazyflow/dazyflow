// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Edges pointing at a port that isn't on the node.
//
// Such an edge is dead three times over: the engine can't carry a value down
// it, React Flow draws nothing for a handle the node doesn't have — so it
// cannot be selected or deleted — and the daemon refuses to save any graph
// containing one. The result is a flow that cannot be saved and shows the
// author nothing to fix, since moving a step is enough to trigger an autosave.
// spawnPort (lib/ports.ts) closed the path that created these; this prunes the
// graphs that already contain one.
//
// It runs against the editor's LIVE state rather than the loaded Graph: at
// graph-load time the drop catalog usually hasn't arrived, so every node's
// manifest is still undefined and there is nothing to judge against.
//
// The rule mirrors core.Validate (core/validate.go) exactly, including what it
// declines to judge, so this never removes an edge the daemon would have
// accepted: a node whose manifest this client doesn't have, an edge touching a
// disabled node, and a step whose ports come from its own params
// (dynamic_ports) are all left alone.

import type { Manifest } from "../types";

export type NodePorts = {
  manifest?: Manifest;
  disabled?: boolean;
};

export type EdgeEnds = {
  source: string;
  sourceHandle?: string | null;
  target: string;
  targetHandle?: string | null;
};

export type StrayEdge = {
  // Which end is wrong, and the port that isn't there — the words the notice
  // shows the author, so they can tell which wire went.
  end: "from" | "to";
  nodeID: string;
  port: string;
  module?: string;
};

function strayReason(
  e: EdgeEnds,
  nodes: Map<string, NodePorts>,
): StrayEdge | null {
  const src = nodes.get(e.source);
  const dst = nodes.get(e.target);
  // An edge naming a node that isn't on the canvas is a different defect, and
  // not one deleting the edge would explain; leave it to the validator.
  if (!src || !dst) return null;
  if (!src.manifest || !dst.manifest) return null;
  if (src.disabled || dst.disabled) return null;
  if (src.manifest.dynamic_ports || dst.manifest.dynamic_ports) return null;

  const fromPort = e.sourceHandle ?? "out";
  const toPort = e.targetHandle ?? "in";
  if (!(src.manifest.outputs ?? []).some((p) => p.port === fromPort)) {
    return { end: "from", nodeID: e.source, port: fromPort, module: src.manifest.id };
  }
  if (!(dst.manifest.inputs ?? []).some((p) => p.port === toPort)) {
    return { end: "to", nodeID: e.target, port: toPort, module: dst.manifest.id };
  }
  return null;
}

// findStrayEdges returns the dead edges among `edges`, in order, each paired
// with the edge it describes. Empty — the overwhelmingly common case — means
// the caller has nothing to do and should not touch state.
export function findStrayEdges<E extends EdgeEnds>(
  edges: readonly E[],
  nodes: Map<string, NodePorts>,
): { edge: E; reason: StrayEdge }[] {
  const out: { edge: E; reason: StrayEdge }[] = [];
  for (const edge of edges) {
    const reason = strayReason(edge, nodes);
    if (reason) out.push({ edge, reason });
  }
  return out;
}
