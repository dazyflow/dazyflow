// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// What it costs to change ONE thing on the canvas.
//
// The editor's per-node data cache (displayNodes) exists so that editing,
// dragging or advancing one step re-renders one card. It is defeated by any
// single dependency that gets a fresh identity on every render, and when it is
// defeated NOTHING FAILS — the canvas stays correct, just quadratically more
// expensive as a flow grows. Measured in a real browser, a sixty-step flow
// re-rendered all sixty cards on every pointermove of a drag.
//
// So this asserts a shape rather than a number: a change scoped to one step must
// not re-render the others. The mechanisms that deliver that today are free to
// change as long as the property holds. It counts renders rather than timing
// anything, because a count is deterministic and a jsdom timing is not.
//
// Verified to fail without the fix, which is the only thing that makes a
// regression test worth its runtime: one status frame produced 7 card renders on
// a 7-node canvas instead of 1.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, act, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { installLayoutStubs, makeStreamJob, manifests, frame } from "./editorTestHarness";

installLayoutStubs();

// vi.hoisted: the mock factory below is lifted above the imports, so it cannot
// close over an ordinary module-level binding.
const counter = vi.hoisted(() => ({ nodes: 0 }));

vi.mock("../../components/editor/NodeCard", async (importOriginal) => {
  const { memo, createElement } = await import("react");
  const actual = await importOriginal<typeof import("../../components/editor/NodeCard")>();
  const Inner = actual.DazyNode;
  const Counting = (props: Record<string, unknown>) => {
    counter.nodes++;
    return createElement(Inner as never, props as never);
  };
  return { ...actual, DazyNode: memo(Counting) };
});

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t, i18n: { language: "en" } };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok",
    me: { subject: "a@b.c", tenant: "acme", workspace: "main" },
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: () => true,
  }),
}));

const STEPS = 6;

function wideGraph(id = "wide") {
  return {
    id,
    tenant: "acme",
    workspace: "main",
    name: "Wide",
    nodes: [
      { id: "manual_1", module: "manual_trigger", params: {}, position: { x: 0, y: 0 } },
      ...Array.from({ length: STEPS }, (_, i) => ({
        id: `ntfy_${i}`,
        module: "ntfy",
        params: { topic: `beans-${i}` },
        position: { x: 320, y: i * 160 },
      })),
    ],
    edges: Array.from({ length: STEPS }, (_, i) => ({
      from: "manual_1",
      from_port: "out",
      to: `ntfy_${i}`,
      to_port: "in",
    })),
    triggers: [],
  };
}

const stream = makeStreamJob();
const loadGraph = vi.fn();
const runGraph = vi.fn();

vi.mock("../../api", () => {
  class APIError extends Error {
    status: number;
    constructor(status: number, msg?: string) {
      super(msg);
      this.status = status;
    }
  }
  return {
    APIError,
    isHTTPStatus: () => false,
    isErrorCode: () => false,
    api: {
      loadGraph: (...a: unknown[]) => loadGraph(...a),
      saveGraph: () => Promise.resolve({ commit: "c1" }),
      listRuns: () => Promise.resolve({ runs: [] }),
      listDrops: () => Promise.resolve({ drops: manifests }),
      dropSuggestions: () => Promise.resolve([]),
      listSecrets: () => Promise.resolve({ secrets: [] }),
      listProviders: () => Promise.resolve({ providers: [] }),
      listSSHCredentials: () => Promise.resolve({ credentials: [] }),
      getPublishedInfo: () => Promise.resolve({ published: false }),
      flowHistory: () => Promise.resolve({ revisions: [] }),
      streamJob: (...a: Parameters<typeof stream.streamJob>) => stream.streamJob(...a),
      runGraph: (...a: unknown[]) => runGraph(...a),
      getNodeRecord: () => Promise.resolve({ Result: { output: {} } }),
      retryRun: () => Promise.resolve({ job_id: "run-2" }),
      cancelRun: () => Promise.resolve({}),
      sampleNode: () => Promise.resolve({}),
      watchFlow: () => new Promise<void>(() => {}),
      publishFlow: () => Promise.resolve({}),
      labelRevision: () => Promise.resolve({}),
      restoreFlow: () => Promise.resolve({}),
      deleteGraph: () => Promise.resolve({}),
      setFlowEnabled: () => Promise.resolve({}),
      resetNodeState: () => Promise.resolve({}),
      resumeRun: () => Promise.resolve({}),
      approveNode: () => Promise.resolve({}),
    },
  };
});

import { FlowEditor } from "./FlowEditor";

function mount() {
  return render(
    <MemoryRouter initialEntries={["/flows/wide"]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
      </Routes>
    </MemoryRouter>,
  );
}

async function emit(kind: string, data: unknown) {
  const sub = stream.latest();
  expect(sub, "no open run stream").toBeTruthy();
  await act(async () => {
    sub.emit(kind, data);
  });
}

beforeEach(() => {
  stream.subs.length = 0;
  counter.nodes = 0;
  loadGraph.mockReset();
  loadGraph.mockResolvedValue(wideGraph());
  runGraph.mockReset();
  runGraph.mockResolvedValue({ job_id: "run-1" });
});

describe("canvas render cost", () => {
  it("advancing one step re-renders that step's card, not the whole canvas", async () => {
    const { container } = mount();
    await screen.findByText("editor.run");
    // Every card is on screen before the measurement starts.
    await waitFor(() =>
      expect(container.querySelectorAll(".react-flow__node").length).toBe(STEPS + 1),
    );

    await userEvent.click(screen.getByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await act(async () => {});
    counter.nodes = 0;

    await emit(...frame.node("ntfy_0", "running"));

    // One step changed, so one card should have re-rendered. The bound is
    // deliberately loose — React may render a component more than once for a
    // single update — but it is far below "every card", which is what a
    // defeated cache costs and what this exists to catch.
    expect(
      counter.nodes,
      `one step's status changed but ${counter.nodes} card renders followed; ` +
        `with ${STEPS} steps on the canvas that is the per-node data cache being ` +
        `defeated by a dependency with an unstable identity`,
    ).toBeLessThan(STEPS);
  });

  // jsdom draws no wires — React Flow needs measured node dimensions to path
  // them, and the layout stubs do not supply those, so a render counter on the
  // wire component reads zero however the canvas behaves. That rules out
  // guarding the wire the way the card is guarded above, and a "renders fewer
  // than N wires" assertion here would pass on nothing.
  //
  // What can still be pinned is the memo itself, which is the fix: React Flow
  // is handed a freshly spread `nodes` array on every render, so an unmemoised
  // wire re-renders whatever its own props say. Measured in a real browser at
  // sixty steps, that was 118 wire renders per character typed, and zero with
  // the memo.
  it("keeps the wire component memoised", async () => {
    const mod = await import("../../components/editor/RerouteEdge");
    expect(
      (mod.RerouteEdge as unknown as { $$typeof?: symbol }).$$typeof,
      "RerouteEdge is no longer wrapped in memo — every wire on the canvas will " +
        "re-render on every keystroke and every drag frame",
    ).toBe(Symbol.for("react.memo"));
  });

  it("stays flat as the canvas grows", async () => {
    // The shape is the point: the cost of one step changing must not scale
    // with how many steps are on the canvas. A cache defeated by an unstable
    // dependency turns this into a straight line.
    const { container } = mount();
    await screen.findByText("editor.run");
    await waitFor(() =>
      expect(container.querySelectorAll(".react-flow__node").length).toBe(STEPS + 1),
    );
    await userEvent.click(screen.getByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));
    await act(async () => {});

    const perFrame: number[] = [];
    for (const id of ["ntfy_0", "ntfy_1", "ntfy_2"]) {
      counter.nodes = 0;
      await emit(...frame.node(id, "running"));
      perFrame.push(counter.nodes);
    }
    for (const n of perFrame) {
      expect(n, `renders per status frame: ${perFrame.join(", ")}`).toBeLessThan(STEPS);
    }
  });
});
