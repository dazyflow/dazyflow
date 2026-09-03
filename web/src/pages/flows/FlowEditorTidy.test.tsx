// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tidy, in the pinned canvas Controls cluster.
//
// It has now been reported invisible twice. The first time it was in the
// scrolling half of the toolbar and genuinely off-screen (see
// FlowEditorToolbarOverflow.test.tsx), which moved it here. The second time it
// was present the whole while: on a flow with fewer than two steps it greyed
// out at 40% opacity, and 40% on a 1px stroked glyph over the pale canvas grid
// does not read as "unavailable", it reads as "absent" — so it vanished
// precisely on the new flows whose author had not yet found it.
//
// So the requirement is now blunt, and this is the guard on it: the control is
// always rendered and never disabled, on every flow, however small.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { installLayoutStubs, makeStreamJob, manifests, twoStepGraph } from "./editorTestHarness";

installLayoutStubs();

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t, i18n: { language: "en" } };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
vi.mock("../../auth", () => {
  const auth = {
    token: "tok",
    me: { subject: "a@b.c", tenant: "acme", workspace: "main" },
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: () => true,
  };
  return { useAuth: () => auth };
});

const stream = makeStreamJob();
const loadGraph = vi.fn();

vi.mock("../../api", () => {
  class APIError extends Error {
    status: number;
    constructor(status: number, msg?: string) {
      super(msg);
      this.status = status;
    }
  }
  const statusOf = (e: unknown) => (e as { status?: number } | null)?.status;
  return {
    APIError,
    isHTTPStatus: (e: unknown, status: number) => statusOf(e) === status,
    isErrorCode: () => false,
    api: {
      loadGraph: (...a: unknown[]) => loadGraph(...a),
      saveGraph: () => Promise.resolve({ commit: "c1" }),
      listRuns: () => Promise.resolve({ runs: [] }),
      listDrops: () => Promise.resolve({ drops: manifests }),
      dropSuggestions: () => Promise.resolve([]),
      listSecrets: () => Promise.resolve({ secrets: [] }),
      listProviders: () => Promise.resolve({ providers: [] }),
      getPublishedInfo: () => Promise.resolve({ published: false }),
      flowHistory: () => Promise.resolve({ revisions: [] }),
      streamJob: (...a: Parameters<typeof stream.streamJob>) => stream.streamJob(...a),
      runGraph: () => Promise.resolve({ job_id: "run-1" }),
      getNodeRecord: () => Promise.resolve({ Result: { output: {} } }),
      retryRun: () => Promise.resolve({ job_id: "run-2" }),
      cancelRun: () => Promise.resolve({}),
      sampleNode: () => Promise.resolve({}),
      watchFlow: () => Promise.resolve({}),
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

function mount(id = "coffee-reorder") {
  return render(
    <MemoryRouter initialEntries={[`/flows/${id}`]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
      </Routes>
    </MemoryRouter>,
  );
}

const tidy = () => screen.findByRole("button", { name: "editor.tidy" });

describe("Tidy control", () => {
  beforeEach(() => {
    loadGraph.mockReset();
  });

  it("is on the canvas for an ordinary flow", async () => {
    loadGraph.mockResolvedValue(twoStepGraph());
    mount();
    expect(await tidy()).toBeEnabled();
  });

  // The regression. Fewer than two steps is exactly when an author is still
  // learning the canvas, so it is the worst possible moment for the control to
  // fade out — and autoLayout already no-ops below two nodes, so there was
  // never anything to protect against.
  it("stays live on a one-step flow", async () => {
    const g = twoStepGraph();
    loadGraph.mockResolvedValue({ ...g, nodes: [g.nodes[0]], edges: [] });
    mount();
    expect(await tidy()).toBeEnabled();
  });

  it("stays live on a flow with no steps at all", async () => {
    loadGraph.mockResolvedValue({ ...twoStepGraph(), nodes: [], edges: [] });
    mount();
    expect(await tidy()).toBeEnabled();
  });

  // It belongs to the pinned cluster, not the scrolling toolbar. That is the
  // whole reason it is reachable at any width, so if it ever moves back the
  // first report comes true again.
  it("lives in the pinned Controls cluster, not the scrolling toolbar", async () => {
    loadGraph.mockResolvedValue(twoStepGraph());
    mount();
    const btn = await tidy();
    expect(btn.closest(".react-flow__controls")).not.toBeNull();
    expect(btn.closest(".toolbar-scroll")).toBeNull();
  });
});
