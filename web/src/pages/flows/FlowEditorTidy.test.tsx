// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tidy — in the pinned toolbar AND in the canvas Controls cluster.
//
// Reported missing three times. Twice it was fixed by changing a mechanism and
// came back: first it lived in the scrolling half of the toolbar and was
// genuinely off-screen, so it moved to the canvas; then it greyed out below two
// steps at 40% opacity, which on a 1px stroked glyph reads as absent rather than
// unavailable, so the dimming went. After both, it was measurably on screen —
// and someone still could not find it, because the canvas cluster gives it no
// NAME.
//
// The guards, therefore: both controls always render, neither is ever disabled
// however small the flow, the toolbar one is never in the scrolling region, and
// it carries a visible label.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
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
      listSSHCredentials: () => Promise.resolve({ credentials: [] }),
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

const canvasTidy = async () => {
  await screen.findAllByRole("button", { name: "editor.tidy" });
  const el = document.querySelector(".dz-tidy-control");
  if (!el) throw new Error("canvas Tidy control not rendered");
  return el as HTMLButtonElement;
};
const pinnedTidy = async () => {
  await screen.findAllByRole("button", { name: "editor.tidy" });
  const el = document.querySelector('[data-tidy="toolbar"]');
  if (!el) throw new Error("pinned toolbar Tidy control not rendered");
  return el as HTMLButtonElement;
};

describe("Tidy control", () => {
  beforeEach(() => {
    loadGraph.mockReset();
  });

  it("is on the canvas for an ordinary flow", async () => {
    loadGraph.mockResolvedValue(twoStepGraph());
    mount();
    expect(await canvasTidy()).toBeEnabled();
  });

  // The second regression. Fewer than two steps is exactly when an author is
  // still learning the canvas, so it is the worst possible moment for the
  // control to fade out — and autoLayout already no-ops below two nodes, so
  // there was never anything to protect against.
  it("stays live on a one-step flow", async () => {
    const g = twoStepGraph();
    loadGraph.mockResolvedValue({ ...g, nodes: [g.nodes[0]], edges: [] });
    mount();
    expect(await canvasTidy()).toBeEnabled();
    expect(await pinnedTidy()).toBeEnabled();
  });

  it("stays live on a flow with no steps at all", async () => {
    loadGraph.mockResolvedValue({ ...twoStepGraph(), nodes: [], edges: [] });
    mount();
    expect(await canvasTidy()).toBeEnabled();
    expect(await pinnedTidy()).toBeEnabled();
  });

  it("lives in the pinned Controls cluster, not the scrolling toolbar", async () => {
    loadGraph.mockResolvedValue(twoStepGraph());
    mount();
    const btn = await canvasTidy();
    expect(btn.closest(".react-flow__controls")).not.toBeNull();
    expect(btn.closest(".toolbar-scroll")).toBeNull();
  });

  it("is also in the toolbar, carrying a visible label", async () => {
    loadGraph.mockResolvedValue(twoStepGraph());
    mount();
    const btn = await pinnedTidy();
    expect(btn).toBeEnabled();
    const label = btn.querySelector(".toolbar-label");
    expect(label).not.toBeNull();
    expect(label?.textContent).toBe("editor.tidy");
  });

  it("the toolbar copy is pinned, never in the scrolling region", async () => {
    loadGraph.mockResolvedValue(twoStepGraph());
    mount();
    const btn = await pinnedTidy();
    expect(btn.closest(".toolbar-scroll")).toBeNull();
    expect(btn.closest(".editor-toolbar")).not.toBeNull();
  });

  // A locked card says on its face that it will not move; Tidy is the one
  // action that moves everything at once, so it is where that breaks.
  it("leaves a locked step exactly where it was", async () => {
    const g = twoStepGraph();
    loadGraph.mockResolvedValue({
      ...g,
      nodes: [
        g.nodes[0],
        { ...g.nodes[1], position: { x: 900, y: 640 }, locked: true },
      ],
    });
    const { container } = mount();
    const btn = await pinnedTidy();
    await waitFor(() =>
      expect(container.querySelectorAll(".react-flow__node").length).toBe(2),
    );
    const posOf = (id: string) =>
      (container.querySelector(`.react-flow__node[data-id="${id}"]`) as HTMLElement | null)
        ?.style.transform;
    const before = { trigger: posOf("manual_1"), locked: posOf("ntfy_1") };
    expect(before.locked).toContain("900");
    await act(async () => {
      btn.click();
    });
    expect(posOf("ntfy_1")).toBe(before.locked);
    expect(posOf("manual_1")).not.toBe(before.trigger);
  });
});
