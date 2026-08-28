// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import {
  COALESCE_WINDOW_MS,
  HISTORY_LIMIT,
  canRedo,
  canUndo,
  classifyDelta,
  emptyHistory,
  rebase,
  record,
  redo,
  undo,
  type HistoryDoc,
} from "./graphHistory";

/** A minimal but realistic document. */
function doc(over: Partial<HistoryDoc> = {}): HistoryDoc {
  return {
    id: "f1",
    tenant: "acme",
    workspace: "main",
    nodes: [
      { id: "a", module: "http_request", params: { url: "https://x.test" }, position: { x: 0, y: 0 } },
      { id: "b", module: "slack_send_message", params: { channel: "#ops" }, position: { x: 240, y: 0 } },
    ],
    edges: [{ from: "a", from_port: "out", to: "b", to_port: "in" }],
    ...over,
  } as HistoryDoc;
}

function moveNode(d: HistoryDoc, id: string, x: number, y: number): HistoryDoc {
  return {
    ...d,
    nodes: (d.nodes ?? []).map((n) => (n.id === id ? { ...n, position: { x, y } } : n)),
  };
}

function setParam(d: HistoryDoc, id: string, key: string, value: unknown): HistoryDoc {
  return {
    ...d,
    nodes: (d.nodes ?? []).map((n) =>
      n.id === id ? { ...n, params: { ...(n.params ?? {}), [key]: value } } : n,
    ),
  };
}

describe("classifyDelta", () => {
  it("reports none for an identical document", () => {
    expect(classifyDelta(doc(), doc())).toEqual({ kind: "none" });
  });

  it("reports none when only a field we don't serialize differs", () => {
    // Selection lives on the React Flow node, never in the document — so a
    // selection change must not register as an edit. This is the case that
    // fires constantly: React Flow rebuilds the node array to flag selection.
    const a = doc();
    const b = JSON.parse(JSON.stringify(a)) as HistoryDoc;
    expect(classifyDelta(a, b)).toEqual({ kind: "none" });
  });

  it("reports positions for a drag", () => {
    expect(classifyDelta(doc(), moveNode(doc(), "a", 40, 12))).toEqual({ kind: "positions" });
  });

  it("reports a specific param for a single field edit", () => {
    expect(classifyDelta(doc(), setParam(doc(), "b", "channel", "#alerts"))).toEqual({
      kind: "param",
      nodeID: "b",
      key: "channel",
    });
  });

  it("reports structure when a node is added or removed", () => {
    const added = doc({
      nodes: [...(doc().nodes ?? []), { id: "c", module: "delay", params: {}, position: { x: 0, y: 0 } }],
    });
    expect(classifyDelta(doc(), added)).toEqual({ kind: "structure" });
    expect(classifyDelta(added, doc())).toEqual({ kind: "structure" });
  });

  it("reports structure when an edge changes", () => {
    expect(classifyDelta(doc(), doc({ edges: [] }))).toEqual({ kind: "structure" });
  });

  it("does not misfile a param edit bundled with a structural change", () => {
    // Coalescing a compound change would merge a delete into a keystroke.
    const both = setParam(doc({ edges: [] }), "b", "channel", "#alerts");
    expect(classifyDelta(doc(), both)).toEqual({ kind: "structure" });
  });

  it("reports two params changing at once as structure, not a coalescible edit", () => {
    const two = setParam(setParam(doc(), "a", "url", "https://y.test"), "b", "channel", "#x");
    expect(classifyDelta(doc(), two)).toEqual({ kind: "structure" });
  });

  it("reports a meta field edit", () => {
    expect(classifyDelta(doc(), doc({ name: "Renamed" }))).toEqual({
      kind: "meta",
      field: "name",
    });
  });

  it("treats a reorder of the same nodes as structure", () => {
    const reversed = doc({ nodes: [...(doc().nodes ?? [])].reverse() });
    expect(classifyDelta(doc(), reversed)).toEqual({ kind: "structure" });
  });
});

describe("record", () => {
  it("takes the first document as a baseline, not an edit", () => {
    const s = record(emptyHistory(), doc(), 1000);
    expect(s.present).toEqual(doc());
    expect(canUndo(s)).toBe(false);
  });

  it("ignores a no-op change and returns the same state object", () => {
    const s1 = record(emptyHistory(), doc(), 1000);
    const s2 = record(s1, doc(), 1100);
    // Reference equality matters: the caller uses it to skip a re-render.
    expect(s2).toBe(s1);
  });

  it("pushes a structural edit", () => {
    let s = record(emptyHistory(), doc(), 1000);
    s = record(s, doc({ edges: [] }), 1100);
    expect(canUndo(s)).toBe(true);
    expect(s.past).toHaveLength(1);
  });

  it("coalesces a drag into one step", () => {
    let s = record(emptyHistory(), doc(), 1000);
    // A drag arrives as many position updates a few ms apart.
    for (let i = 1; i <= 25; i++) {
      s = record(s, moveNode(doc(), "a", i * 4, 0), 1000 + i * 16);
    }
    expect(s.past).toHaveLength(1); // one undo step for the whole drag
    const back = undo(s)!;
    expect(back.doc).toEqual(doc()); // lands where the drag started
  });

  it("coalesces keystrokes in one field into one step", () => {
    let s = record(emptyHistory(), doc(), 1000);
    const typed = "#alerts";
    for (let i = 1; i <= typed.length; i++) {
      s = record(s, setParam(doc(), "b", "channel", typed.slice(0, i)), 1000 + i * 40);
    }
    expect(s.past).toHaveLength(1);
    expect(undo(s)!.doc).toEqual(doc());
  });

  it("starts a new step after a pause longer than the window", () => {
    let s = record(emptyHistory(), doc(), 1000);
    s = record(s, moveNode(doc(), "a", 40, 0), 1016);
    s = record(s, moveNode(doc(), "a", 80, 0), 1016 + COALESCE_WINDOW_MS + 1);
    expect(s.past).toHaveLength(2);
  });

  it("does not coalesce edits to different fields of the same node", () => {
    let s = record(emptyHistory(), doc(), 1000);
    s = record(s, setParam(doc(), "a", "url", "https://y.test"), 1010);
    s = record(s, setParam(setParam(doc(), "a", "url", "https://y.test"), "a", "method", "POST"), 1020);
    expect(s.past).toHaveLength(2);
  });

  it("does not coalesce two structural edits", () => {
    let s = record(emptyHistory(), doc(), 1000);
    s = record(s, doc({ edges: [] }), 1010);
    s = record(s, doc({ edges: [], nodes: [(doc().nodes ?? [])[0]] }), 1020);
    expect(s.past).toHaveLength(2);
  });

  it("does not coalesce a drag with a following delete", () => {
    let s = record(emptyHistory(), doc(), 1000);
    s = record(s, moveNode(doc(), "a", 40, 0), 1010);
    s = record(s, moveNode(doc({ edges: [] }), "a", 40, 0), 1020);
    expect(s.past).toHaveLength(2);
  });

  it("bounds the stack, dropping the oldest", () => {
    let s = record(emptyHistory(), doc(), 0);
    for (let i = 1; i <= HISTORY_LIMIT + 40; i++) {
      // Structural each time, spaced past the window, so nothing coalesces.
      s = record(s, doc({ name: `n${i}`, edges: i % 2 ? [] : doc().edges }), i * 5000);
    }
    expect(s.past.length).toBe(HISTORY_LIMIT);
  });
});

describe("undo / redo", () => {
  it("returns null when there is nothing to do", () => {
    const s = record(emptyHistory(), doc(), 1000);
    expect(undo(s)).toBeNull();
    expect(redo(s)).toBeNull();
  });

  it("round-trips a single edit", () => {
    let s = record(emptyHistory(), doc(), 1000);
    const edited = doc({ edges: [] });
    s = record(s, edited, 2000);

    const u = undo(s)!;
    expect(u.doc).toEqual(doc());
    expect(canRedo(u.state)).toBe(true);

    const r = redo(u.state)!;
    expect(r.doc).toEqual(edited);
    expect(canUndo(r.state)).toBe(true);
    expect(canRedo(r.state)).toBe(false);
  });

  it("walks back through several steps in order", () => {
    const v0 = doc();
    const v1 = doc({ name: "one" });
    const v2 = doc({ name: "two", edges: [] });
    let s = record(emptyHistory(), v0, 1000);
    s = record(s, v1, 9000);
    s = record(s, v2, 20000);

    let u = undo(s)!;
    expect(u.doc).toEqual(v1);
    u = undo(u.state)!;
    expect(u.doc).toEqual(v0);
    expect(undo(u.state)).toBeNull();

    let r = redo(u.state)!;
    expect(r.doc).toEqual(v1);
    r = redo(r.state)!;
    expect(r.doc).toEqual(v2);
  });

  it("drops the redo branch once a new edit lands", () => {
    let s = record(emptyHistory(), doc(), 1000);
    s = record(s, doc({ edges: [] }), 2000);
    const u = undo(s)!;
    expect(canRedo(u.state)).toBe(true);

    const s2 = record(u.state, doc({ name: "divergent" }), 3000);
    expect(canRedo(s2)).toBe(false);
  });

  it("clears the redo branch when a coalescing edit lands too", () => {
    // The merge path returns early, so it needs its own assertion — a stale
    // redo branch after a merged edit would replay a state the user has
    // already diverged from.
    let s = record(emptyHistory(), doc(), 1000);
    s = record(s, moveNode(doc(), "a", 40, 0), 2000);
    const u = undo(s)!;
    expect(canRedo(u.state)).toBe(true);
    const merged = record(u.state, moveNode(doc(), "a", 8, 0), 2100);
    expect(canRedo(merged)).toBe(false);
  });
});

describe("rebase", () => {
  it("adopts a new baseline and forgets both directions", () => {
    let s = record(emptyHistory(), doc(), 1000);
    s = record(s, doc({ edges: [] }), 2000);
    const u = undo(s)!;
    expect(canUndo(u.state) || canRedo(u.state)).toBe(true);

    // An external edit landed (MCP flow-watch) — undoing past it would
    // clobber someone else's change, so the stack is fenced.
    const fenced = rebase(doc({ name: "from the assistant" }), 3000);
    expect(canUndo(fenced)).toBe(false);
    expect(canRedo(fenced)).toBe(false);
    expect(fenced.present).toEqual(doc({ name: "from the assistant" }));
  });
});
