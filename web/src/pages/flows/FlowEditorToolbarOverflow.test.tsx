// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The editor toolbar's overflow affordance.
//
// The bar's left half scrolls and is handed whatever width the pinned actions
// (Run, Publish) leave it, which on a laptop with the Inspector open is not
// enough for all the secondary tools. Its scrollbar is hidden by design, so
// without a per-side arrow the tools out of view are reachable only by
// guessing that the bar swipes sideways — which is how Tidy became invisible.
//
// jsdom has no layout, so the geometry the measurement reads is stubbed here.
// That is the point: the arrows exist for a size the other editor tests never
// reach, and nothing about their absence is visible from the outside.

import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, act } from "@testing-library/react";
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

// The scroll region's geometry, readable and writable by the test. Only
// .toolbar-scroll reports it; every other element keeps jsdom's zero, so React
// Flow and the rest of the editor behave exactly as in the other suites.
const GEOM_PROPS = ["clientWidth", "scrollWidth", "scrollLeft"] as const;
const geom = { clientWidth: 0, scrollWidth: 0, scrollLeft: 0 };
const savedDescriptors = new Map<string, PropertyDescriptor | undefined>();

function installToolbarGeometry() {
  for (const prop of GEOM_PROPS) {
    savedDescriptors.set(prop, Object.getOwnPropertyDescriptor(Element.prototype, prop));
    Object.defineProperty(Element.prototype, prop, {
      configurable: true,
      get(this: Element) {
        return this.classList?.contains("toolbar-scroll") ? geom[prop] : 0;
      },
      set() {},
    });
  }
}

function restoreToolbarGeometry() {
  for (const prop of GEOM_PROPS) {
    const d = savedDescriptors.get(prop);
    if (d) Object.defineProperty(Element.prototype, prop, d);
    else delete (Element.prototype as unknown as Record<string, unknown>)[prop];
  }
  savedDescriptors.clear();
}

const scrollBy = vi.fn();

function mount(id = "coffee-reorder") {
  return render(
    <MemoryRouter initialEntries={[`/flows/${id}`]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
      </Routes>
    </MemoryRouter>,
  );
}

const scrollRegion = () => document.querySelector(".toolbar-scroll") as HTMLElement;

// What the browser does after a smooth scroll settles: the position moves and
// the element fires `scroll`. The component re-measures off that event, not off
// a render, so a test that skips it would never see the arrows swap sides.
async function settleScrollAt(left: number) {
  await act(async () => {
    geom.scrollLeft = left;
    scrollRegion().dispatchEvent(new Event("scroll"));
  });
}

describe("editor toolbar overflow", () => {
  beforeEach(() => {
    loadGraph.mockReset();
    loadGraph.mockResolvedValue(twoStepGraph());
    scrollBy.mockReset();
    Element.prototype.scrollBy = scrollBy;
    geom.clientWidth = 0;
    geom.scrollWidth = 0;
    geom.scrollLeft = 0;
    installToolbarGeometry();
  });
  afterEach(() => {
    restoreToolbarGeometry();
    vi.restoreAllMocks();
  });

  it("offers no arrows when every tool fits", async () => {
    geom.clientWidth = 900;
    geom.scrollWidth = 900;
    mount();
    await screen.findByText("editor.run");
    expect(screen.queryByRole("button", { name: "editor.toolbarMoreLeft" })).toBeNull();
    expect(screen.queryByRole("button", { name: "editor.toolbarMoreRight" })).toBeNull();
    // No fade either: a permanent one dimmed the trailing control on a bar
    // that had nothing hidden.
    expect(scrollRegion().dataset.fadeRight).toBe("false");
  });

  it("points at the tools it is hiding, on the side they are hidden", async () => {
    geom.clientWidth = 200;
    geom.scrollWidth = 700;
    mount();
    await screen.findByRole("button", { name: "editor.toolbarMoreRight" });
    // Nothing is off-screen to the LEFT yet, so that arrow would be a lie.
    expect(screen.queryByRole("button", { name: "editor.toolbarMoreLeft" })).toBeNull();
    expect(scrollRegion().dataset.fadeRight).toBe("true");
    expect(scrollRegion().dataset.fadeLeft).toBe("false");
  });

  it("scrolls towards the hidden tools, then offers the way back", async () => {
    geom.clientWidth = 200;
    geom.scrollWidth = 700;
    mount();
    const right = await screen.findByRole("button", { name: "editor.toolbarMoreRight" });
    await act(async () => {
      right.click();
    });
    expect(scrollBy).toHaveBeenCalledWith(
      expect.objectContaining({ left: 160, behavior: "smooth" }),
    );

    await settleScrollAt(160);
    // Both sides now have tools out of view, so both arrows stand.
    await screen.findByRole("button", { name: "editor.toolbarMoreLeft" });
    expect(screen.getByRole("button", { name: "editor.toolbarMoreRight" })).toBeTruthy();
    expect(scrollRegion().dataset.fadeLeft).toBe("true");

    // At the far end the right-hand arrow retires — there is nothing left to
    // reveal, and an arrow that scrolls nowhere is worse than none.
    await settleScrollAt(500);
    await act(async () => {});
    expect(screen.queryByRole("button", { name: "editor.toolbarMoreRight" })).toBeNull();
    expect(screen.getByRole("button", { name: "editor.toolbarMoreLeft" })).toBeTruthy();
  });
});
