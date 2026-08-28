// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Mirrors core/behavior_test.go. The two must agree on what counts as a
// publishable change: the toolbar prompts to publish from the daemon's answer
// (core.BehaviorEqual) and this view explains what changed. When they
// disagree the editor contradicts itself — "you have unpublished changes"
// next to "your draft matches the published version".
import { describe, expect, it } from "vitest";
import { diffGraphs, diffIsEmpty } from "./diffGraphs";
import type { Graph } from "../types";

function fixture(): Graph {
  return {
    id: "f1",
    tenant: "t",
    workspace: "ws",
    name: "Nightly report",
    nodes: [
      {
        id: "a",
        module: "cron_trigger",
        params: { cron: "0 9 * * *" },
        position: { x: 10, y: 20 },
      },
      { id: "b", module: "slack_post", params: { channel: "#ops" } },
    ],
    edges: [{ from: "a", from_port: "out", to: "b", to_port: "in" }],
  };
}

// Each of these is a canvas gesture, not a change to what the flow does.
const cosmetic: [string, (g: Graph) => void][] = [
  ["moved step", (g) => void (g.nodes[0].position = { x: 900, y: 900 })],
  ["step renamed", (g) => void (g.nodes[0].label = "Every morning")],
  ["position dropped", (g) => void (g.nodes[0].position = undefined)],
  [
    "note added",
    (g) => void (g.frames = [{ id: "fr1", title: "Morning", x: 0, y: 0, width: 360, height: 240 }]),
  ],
  [
    "wire re-routed",
    (g) =>
      void (g.edges[0].waypoints = [
        { x: 5, y: 5 },
        { x: 50, y: 5 },
      ]),
  ],
  ["paused", (g) => void (g.disabled = true)],
];

// Each of these changes what a run, a schedule, or a trigger does.
const behavioural: [string, (g: Graph) => void][] = [
  ["param edited", (g) => void (g.nodes[0].params.cron = "0 3 * * *")],
  ["module swapped", (g) => void (g.nodes[1].module = "email_send")],
  ["step added", (g) => void g.nodes.push({ id: "c", module: "delay", params: {} })],
  ["step removed", (g) => void g.nodes.pop()],
  ["step switched off", (g) => void (g.nodes[1].disabled = true)],
  ["step made non-critical", (g) => void (g.nodes[1].continue_on_error = true)],
  ["breakpoint set", (g) => void (g.nodes[1].breakpoint = true)],
  ["node timeout set", (g) => void (g.nodes[1].timeout_seconds = 30)],
  ["edge rewired", (g) => void (g.edges[0].to_port = "other")],
  ["edge error policy", (g) => void (g.edges[0].on_error = "fallback")],
  ["edge added", (g) => void g.edges.push({ from: "b", from_port: "out", to: "a", to_port: "in" })],
  ["trigger added", (g) => void (g.triggers = [{ type: "cron", cron: "* * * * *" }])],
  ["renamed", (g) => void (g.name = "Weekly report")],
  ["icon changed", (g) => void (g.icon = "rocket")],
  ["description changed", (g) => void (g.description = "Sends the nightly digest")],
  ["visibility", (g) => void (g.visibility = "private")],
  ["graph timeout", (g) => void (g.timeout_seconds = 600)],
  // Publishing a language change changes the words a run writes, so it has to
  // appear in the diff the publish confirm shows — mirrors core.BehaviorEqual.
  ["output language", (g) => void (g.language = "sv")],
  ["failure notify", (g) => void (g.failure_notify = { webhook: "https://hooks.example/x" })],
];

describe("diffGraphs", () => {
  it("reports no change between identical revisions", () => {
    expect(diffIsEmpty(diffGraphs(fixture(), fixture()))).toBe(true);
  });

  it.each(cosmetic)("ignores an editor-only edit: %s", (_name, edit) => {
    const draft = fixture();
    edit(draft);
    expect(diffIsEmpty(diffGraphs(fixture(), draft))).toBe(true);
  });

  it.each(behavioural)("itemizes a real change: %s", (_name, edit) => {
    const draft = fixture();
    edit(draft);
    const d = diffGraphs(fixture(), draft);
    expect(diffIsEmpty(d)).toBe(false);
    // The catch-all is the safety net for a field nothing itemizes; a change
    // this list names should be described, not swept into "other".
    expect(d.metaChanged).not.toContain("other");
  });

  it("describes which node fields moved", () => {
    const draft = fixture();
    draft.nodes[1].module = "email_send";
    draft.nodes[1].params = { to: "ops@example.com" };
    draft.nodes[1].breakpoint = true;
    const d = diffGraphs(fixture(), draft);
    expect(d.changedNodes).toEqual([
      { id: "b", fields: ["module", "params", "breakpoint"] },
    ]);
  });

  it("falls back to 'other' for a difference nothing itemizes", () => {
    // Stands in for a future field: the version stamp is compared by neither
    // the node walk nor the meta list, and must still surface rather than
    // leaving the view claiming the revisions match.
    const draft = fixture();
    draft.version = "2";
    const d = diffGraphs(fixture(), draft);
    expect(d.metaChanged).toEqual(["other"]);
    expect(diffIsEmpty(d)).toBe(false);
  });
});
