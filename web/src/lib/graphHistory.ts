// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Graph } from "../types";

// Undo/redo for the flow editor.
//
// WHY SNAPSHOTS, NOT COMMANDS
//
// The obvious design for a graph editor is a command stack: every action
// registers a do/undo pair. It rots. The editor's document state has grown to
// nodes, edges, comment frames, per-node params, breakpoints, disabled steps,
// triggers, visibility, owner, name, icon, description and timeout — and the
// next feature adds another. A command stack needs a new inverse for each one,
// and a missing inverse is a silent corruption: undo appears to work and
// quietly drops a field.
//
// Snapshots of the whole document can't rot that way, because the editor
// ALREADY maintains a complete serializer (buildGraph) and deserializer
// (hydrateGraph) — saving and loading depend on them, so they cannot fall
// behind without the flow itself failing to persist. History piggybacks on a
// completeness property the codebase already enforces. Anything buildGraph
// learns to serialize is undoable for free.
//
// The graphs are small (600 B – 5 KB in practice), so whole-document snapshots
// cost far less memory than the bookkeeping a command stack would need.
//
// WHY NOT THE SERVER'S VERSION SNAPSHOTS
//
// They exist and they're whole-graph too, but they're the wrong cadence by
// design: SaveCoalescing AMENDS the previous commit inside a 90-second window
// (workspace/store.go), so an editing burst becomes ONE commit and the
// intermediate states are destroyed rather than stored. They're also a network
// round-trip, and restoring writes a new commit — so undo would append to the
// very history the coalescing exists to keep readable.

/** A document snapshot: the editable graph, with server-lifecycle fields excluded. */
export type HistoryDoc = Graph;

/**
 * How one document differs from the next. Drives coalescing: a continuous
 * gesture (dragging, typing) must collapse into a single undo step, or one
 * Ctrl+Z would rewind a single mouse-move.
 *
 * Deliberately derived from the CONTENT rather than from timing alone, so the
 * behaviour is deterministic and testable. Timing only decides how long a run
 * of same-kind edits may keep merging.
 */
export type DeltaKind =
  | { kind: "none" }
  /** Only node/frame positions moved — a drag. */
  | { kind: "positions" }
  /** One param of one node changed — typing in a field. */
  | { kind: "param"; nodeID: string; key: string }
  /** One text field on the flow itself changed — typing in settings. */
  | { kind: "meta"; field: string }
  /** Anything else: add, delete, connect, disconnect, toggle, reorder. */
  | { kind: "structure" };

export interface HistoryEntry {
  doc: HistoryDoc;
  /** What produced this entry, for coalescing the NEXT one against it. */
  delta: DeltaKind;
  /** Wall-clock ms, for the coalescing window. Injected so tests are deterministic. */
  at: number;
}

export interface HistoryState {
  past: HistoryEntry[];
  present: HistoryDoc | null;
  future: HistoryEntry[];
  /** The delta that produced `present`, so the next edit can coalesce against it. */
  presentDelta: DeltaKind;
  presentAt: number;
}

/**
 * How long a run of same-kind edits keeps merging into one undo step. Long
 * enough to swallow a slow drag or unhurried typing; short enough that a
 * deliberate pause starts a fresh step, which is what makes undo predictable.
 */
export const COALESCE_WINDOW_MS = 600;

/**
 * Stack depth. Snapshots are small, so this is generous — the cap exists to
 * bound a pathological session, not to be frugal.
 */
export const HISTORY_LIMIT = 200;

export function emptyHistory(): HistoryState {
  return { past: [], present: null, future: [], presentDelta: { kind: "none" }, presentAt: 0 };
}

/** Cheap structural equality for the doc shapes we snapshot. */
function sameJSON(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  return JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
}

function positionsStripped(g: HistoryDoc): unknown {
  return {
    ...g,
    nodes: (g.nodes ?? []).map((n) => ({ ...n, position: undefined })),
    frames: (g.frames ?? []).map((f) => ({ ...f, x: undefined, y: undefined })),
  };
}

/**
 * classifyDelta reports what changed between two documents.
 *
 * Order matters: the cheapest and most specific tests run first, and
 * "structure" is the fallback so an unrecognized change is never coalesced
 * away. Being wrong in that direction costs an extra undo step; being wrong
 * the other way would silently merge a delete into a drag.
 */
export function classifyDelta(prev: HistoryDoc | null, next: HistoryDoc): DeltaKind {
  if (!prev) return { kind: "structure" };
  if (sameJSON(prev, next)) return { kind: "none" };

  const prevNodes = prev.nodes ?? [];
  const nextNodes = next.nodes ?? [];

  // Same node ids in the same order, and everything except positions equal →
  // a drag.
  if (
    prevNodes.length === nextNodes.length &&
    prevNodes.every((n, i) => n.id === nextNodes[i].id) &&
    sameJSON(positionsStripped(prev), positionsStripped(next))
  ) {
    return { kind: "positions" };
  }

  // Exactly one param of exactly one node differs → typing in a field.
  if (
    prevNodes.length === nextNodes.length &&
    prevNodes.every((n, i) => n.id === nextNodes[i].id)
  ) {
    const changed: { nodeID: string; keys: string[] }[] = [];
    for (let i = 0; i < prevNodes.length; i++) {
      const a = (prevNodes[i].params ?? {}) as Record<string, unknown>;
      const b = (nextNodes[i].params ?? {}) as Record<string, unknown>;
      const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
      const diff = [...keys].filter((k) => !sameJSON(a[k], b[k]));
      if (diff.length) changed.push({ nodeID: prevNodes[i].id, keys: diff });
    }
    if (changed.length === 1 && changed[0].keys.length === 1) {
      // Confirm nothing ELSE moved, so a param edit bundled with a structural
      // change isn't misfiled as coalescible.
      const strip = (g: HistoryDoc) => ({
        ...g,
        nodes: (g.nodes ?? []).map((n) => ({ ...n, params: undefined })),
      });
      if (sameJSON(strip(prev), strip(next))) {
        return { kind: "param", nodeID: changed[0].nodeID, key: changed[0].keys[0] };
      }
    }
  }

  // A single free-text field on the flow itself → typing in the settings modal.
  const metaFields = ["name", "description", "icon"] as const;
  const metaChanged = metaFields.filter((f) => !sameJSON(prev[f], next[f]));
  if (metaChanged.length === 1) {
    const strip = (g: HistoryDoc) => {
      const c: Record<string, unknown> = { ...g };
      for (const f of metaFields) delete c[f];
      return c;
    };
    if (sameJSON(strip(prev), strip(next))) {
      return { kind: "meta", field: metaChanged[0] };
    }
  }

  return { kind: "structure" };
}

/** Two deltas coalesce when they are the same continuous gesture. */
function coalescible(a: DeltaKind, b: DeltaKind): boolean {
  if (a.kind !== b.kind) return false;
  switch (a.kind) {
    case "positions":
      return true;
    case "param":
      return b.kind === "param" && a.nodeID === b.nodeID && a.key === b.key;
    case "meta":
      return b.kind === "meta" && a.field === b.field;
    default:
      // "structure" never coalesces: two deletes are two undo steps, which is
      // what a user expects. "none" never reaches here.
      return false;
  }
}

/**
 * record folds a new document into the history.
 *
 * Returns the same state object when nothing changed, so a caller can use
 * reference equality to skip a re-render — selection-only changes reach here
 * constantly (React Flow rebuilds the node array to flag a selection) and must
 * not create undo steps.
 */
export function record(state: HistoryState, doc: HistoryDoc, now: number): HistoryState {
  if (state.present === null) {
    // First observation is the baseline, not an edit.
    return { past: [], present: doc, future: [], presentDelta: { kind: "none" }, presentAt: now };
  }
  const delta = classifyDelta(state.present, doc);
  if (delta.kind === "none") return state;

  // Merge into the current step instead of pushing, when this is a
  // continuation of the same gesture. `past` is untouched, so one drag or one
  // run of keystrokes stays a single Ctrl+Z.
  if (
    coalescible(state.presentDelta, delta) &&
    now - state.presentAt <= COALESCE_WINDOW_MS
  ) {
    return { ...state, present: doc, presentDelta: delta, presentAt: now, future: [] };
  }

  const past = [
    ...state.past,
    { doc: state.present, delta: state.presentDelta, at: state.presentAt },
  ];
  // Drop the oldest when over the cap.
  while (past.length > HISTORY_LIMIT) past.shift();
  return { past, present: doc, future: [], presentDelta: delta, presentAt: now };
}

export function canUndo(state: HistoryState): boolean {
  return state.past.length > 0;
}
export function canRedo(state: HistoryState): boolean {
  return state.future.length > 0;
}

/**
 * undo steps back one entry. Returns null when there's nothing to undo, so the
 * caller can leave its state untouched rather than re-rendering.
 */
export function undo(state: HistoryState): { state: HistoryState; doc: HistoryDoc } | null {
  if (!state.past.length || state.present === null) return null;
  const past = state.past.slice();
  const prev = past.pop()!;
  return {
    state: {
      past,
      present: prev.doc,
      future: [
        { doc: state.present, delta: state.presentDelta, at: state.presentAt },
        ...state.future,
      ],
      presentDelta: prev.delta,
      presentAt: prev.at,
    },
    doc: prev.doc,
  };
}

export function redo(state: HistoryState): { state: HistoryState; doc: HistoryDoc } | null {
  if (!state.future.length || state.present === null) return null;
  const future = state.future.slice();
  const next = future.shift()!;
  return {
    state: {
      past: [
        ...state.past,
        { doc: state.present, delta: state.presentDelta, at: state.presentAt },
      ],
      present: next.doc,
      future,
      presentDelta: next.delta,
      presentAt: next.at,
    },
    doc: next.doc,
  };
}

/**
 * rebase discards the stack and adopts doc as the new baseline.
 *
 * Called when the document changed underneath the editor and the old stack no
 * longer describes reachable states: a flow switch, a successful reload, a
 * history-revision preview, and — importantly — an external edit arriving over
 * the MCP flow-watch. Undoing past someone else's edit would silently clobber
 * it, so the stack is fenced at that point instead.
 */
export function rebase(doc: HistoryDoc, now: number): HistoryState {
  return { past: [], present: doc, future: [], presentDelta: { kind: "none" }, presentAt: now };
}
