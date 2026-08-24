// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Version history: listing revisions, previewing one on the canvas, leaving the
// preview, and restoring.
//
// The load-bearing idea is that a preview is READ-ONLY. It puts an old revision
// on the canvas without touching HEAD, and the whole editor has to respect that:
// autosave must not fire, the publish switch must be unavailable, and leaving
// the preview must always end with it cleared even if reloading HEAD fails.
// "previewing" is the one autosave guard the existing save tests don't cover, so
// it is pinned here before useRevisions is extracted.

import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok",
    me: { subject: "a@b.c", tenant: "acme", workspace: "main" },
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: () => true,
  }),
}));

const stream = makeStreamJob();
const loadGraph = vi.fn();
const flowHistory = vi.fn();
const restoreFlow = vi.fn();
const saveGraph = vi.fn();

vi.mock("../../api", () => {
  class APIError extends Error {
    status: number;
    code?: string;
    constructor(status: number, msg?: string) {
      super(msg);
      this.status = status;
    }
  }
  const statusOf = (e: unknown) => (e as { status?: number } | null)?.status;
  const codeOf = (e: unknown) => (e as { code?: string } | null)?.code;
  return {
    APIError,
    isHTTPStatus: (e: unknown, s: number) => statusOf(e) === s,
    isErrorCode: (e: unknown, c: string) => codeOf(e) === c,
    api: {
      loadGraph: (...a: unknown[]) => loadGraph(...a),
      flowHistory: (...a: unknown[]) => flowHistory(...a),
      restoreFlow: (...a: unknown[]) => restoreFlow(...a),
      saveGraph: (...a: unknown[]) => saveGraph(...a),
      listDrops: () => Promise.resolve({ drops: manifests }),
      dropSuggestions: () => Promise.resolve([]),
      listSecrets: () => Promise.resolve({ secrets: [] }),
      listProviders: () => Promise.resolve({ providers: [] }),
      listRuns: () => Promise.resolve({ runs: [] }),
      getPublishedInfo: () => Promise.resolve({ published: false }),
      streamJob: (...a: never[]) => stream.streamJob(...a),
      runGraph: () => Promise.resolve({ job_id: "run-1" }),
      getNodeRecord: () => Promise.resolve({ Result: { output: {} } }),
      retryRun: () => Promise.resolve({ job_id: "run-2" }),
      cancelRun: () => Promise.resolve({}),
      sampleNode: () => Promise.resolve({}),
      watchFlow: () => Promise.resolve({}),
      publishFlow: () => Promise.resolve({}),
      labelRevision: () => Promise.resolve({}),
      setFlowEnabled: () => Promise.resolve({}),
      deleteGraph: () => Promise.resolve({}),
      resetNodeState: () => Promise.resolve({}),
      resumeRun: () => Promise.resolve({}),
      approveNode: () => Promise.resolve({}),
      testTrigger: () => Promise.resolve({ job_id: "run-3" }),
    },
  };
});

import { FlowEditor } from "./FlowEditor";

const REVISIONS = [
  { commit: "cccc333", when: "2026-08-24T09:00:00Z", author: "alice@acme.se" },
  { commit: "bbbb222", when: "2026-08-23T09:00:00Z", author: "bob@acme.se" },
  { commit: "aaaa111", when: "2026-08-22T09:00:00Z", author: "carol@acme.se" },
];

function mount(id = "coffee-reorder") {
  return render(
    <MemoryRouter initialEntries={[`/flows/${id}`]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
      </Routes>
    </MemoryRouter>,
  );
}

// Open the history panel and pick a revision. Rows are identified by author,
// which is the only per-revision text a stubbed `t` leaves intact (the dates go
// through the shared formatter, and the newest row reads "editor.historyLatest").
async function openHistoryAndPick(author: string) {
  await userEvent.click(await screen.findByText("editor.history"));
  await userEvent.click(await screen.findByText(author));
}

// A graph carrying a breakpoint, so the editor loads with breakpoints.size > 0
// and the "d" shortcut (clear breakpoints) becomes available. That shortcut is
// the cheapest reachable way to make the graph dirty from a test without
// simulating canvas drags — it sets dirty unconditionally.
function graphWithBreakpoint() {
  const g = twoStepGraph();
  return {
    ...g,
    nodes: g.nodes.map((n) =>
      n.id === "ntfy_1" ? { ...n, breakpoint: true } : n,
    ),
  };
}

// Make the graph dirty. The handler bails on any modifier or when focus sits in
// a text field, so this is deliberately a bare keypress.
async function makeDirty() {
  await userEvent.keyboard("d");
}

beforeEach(() => {
  stream.subs.length = 0;
  loadGraph.mockResolvedValue(twoStepGraph());
  flowHistory.mockResolvedValue({ revisions: REVISIONS, published_commit: "bbbb222" });
  restoreFlow.mockResolvedValue({});
  saveGraph.mockResolvedValue({});
});

describe("editor version history", () => {
  it("lists the flow's revisions when the panel opens", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.history"));
    await waitFor(() => expect(flowHistory).toHaveBeenCalled());
    expect(await screen.findByText("bob@acme.se")).toBeInTheDocument();
    expect(screen.getByText("carol@acme.se")).toBeInTheDocument();
  });

  it("previewing loads that commit and never writes", async () => {
    mount();
    await screen.findByText("editor.run");
    loadGraph.mockClear();
    await openHistoryAndPick("carol@acme.se");
    // The commit is the 5th argument to loadGraph; HEAD loads pass undefined.
    await waitFor(() =>
      expect(loadGraph).toHaveBeenCalledWith("tok", "acme", "main", "coffee-reorder", "aaaa111"),
    );
    expect(saveGraph).not.toHaveBeenCalled();
  });

  it("offers a way back to the live version while previewing", async () => {
    mount();
    await openHistoryAndPick("carol@acme.se");
    expect(await screen.findByText("editor.backToLatest")).toBeInTheDocument();
  });

  it("withdraws the publish switch while previewing", async () => {
    // Publishing from under a preview would push a revision the user isn't
    // looking at.
    mount();
    await screen.findByRole("switch");
    await openHistoryAndPick("carol@acme.se");
    await waitFor(() =>
      expect(screen.getByRole("switch")).toBeDisabled(),
    );
  });

  // The undo stack must not reach past a document the user did not author.
  // hydrateGraph fences it on every outside replacement — a preview, a restore,
  // a flow switch, an external edit arriving over the MCP flow-watch. The last
  // is the one that matters: undoing past someone else's edit would silently
  // discard it.
  //
  // A fresh mount cannot test this, because `record` already treats its first
  // observation as a baseline rather than an edit — deleting the fence does not
  // change what a newly opened flow offers. It takes a stack that exists first,
  // and then a replacement.
  it("fences the undo stack when a revision replaces the document", async () => {
    // makeDirty clears breakpoints, so the flow has to load with one.
    loadGraph.mockResolvedValue(graphWithBreakpoint());
    mount();
    await screen.findByText("editor.run");
    await makeDirty();
    await waitFor(() => expect(screen.getByLabelText("editor.undo")).toBeEnabled());

    await openHistoryAndPick("carol@acme.se");
    await screen.findByText("editor.backToLatest");
    await userEvent.click(screen.getByText("editor.backToLatest"));

    await waitFor(() => expect(screen.getByLabelText("editor.undo")).toBeDisabled());
  });

  it("leaving the preview reloads HEAD", async () => {
    mount();
    await openHistoryAndPick("carol@acme.se");
    loadGraph.mockClear();
    await userEvent.click(await screen.findByText("editor.backToLatest"));
    await waitFor(() =>
      expect(loadGraph).toHaveBeenCalledWith("tok", "acme", "main", "coffee-reorder"),
    );
  });

  it("clears the preview even when reloading HEAD fails", async () => {
    // The reload lives in a try/finally for this reason: a transient fetch error
    // must not strand the editor in a read-only preview with no way out.
    mount();
    await openHistoryAndPick("carol@acme.se");
    await screen.findByText("editor.backToLatest");
    loadGraph.mockRejectedValue(new Error("network"));
    await userEvent.click(screen.getByText("editor.backToLatest"));
    await waitFor(() =>
      expect(screen.queryByText("editor.backToLatest")).not.toBeInTheDocument(),
    );
  });

  it("restoring writes the revision, reloads HEAD and refreshes the list", async () => {
    mount();
    await openHistoryAndPick("carol@acme.se");
    await userEvent.click(await screen.findByText("editor.restore"));
    await waitFor(() =>
      expect(restoreFlow).toHaveBeenCalledWith("tok", "acme", "main", "coffee-reorder", "aaaa111"),
    );
    // History is preserved: a restore is a fresh commit on top, so the list has
    // to be re-fetched rather than assumed unchanged.
    await waitFor(() => expect(flowHistory).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.queryByText("editor.backToLatest")).not.toBeInTheDocument(),
    );
  });
});

describe("preview blocks autosave", () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
  afterEach(() => vi.useRealTimers());

  // The positive control. Without it the guard test below passes for the wrong
  // reason: previewing alone never sets `dirty`, so no autosave would have fired
  // regardless of the guard. The first version of this file did exactly that —
  // it survived deleting the guard outright.
  it("autosaves a dirty graph when NOT previewing", async () => {
    loadGraph.mockResolvedValue(graphWithBreakpoint());
    mount();
    await screen.findByText("editor.run");
    await makeDirty();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    expect(saveGraph).toHaveBeenCalled();
  });

  // The fifth autosave guard, and the one the save tests don't reach: a preview
  // shows an old revision, deliberately different from HEAD. Autosaving while it
  // is on the canvas would quietly make the old version the current one.
  it("does not autosave while a revision is previewed", async () => {
    loadGraph.mockResolvedValue(graphWithBreakpoint());
    mount();
    await openHistoryAndPick("carol@acme.se");
    await screen.findByText("editor.backToLatest");
    saveGraph.mockClear();
    // Dirty the graph WHILE the preview is up — the reachable hazard, since the
    // flag survives opening the history panel.
    await makeDirty();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    expect(saveGraph).not.toHaveBeenCalled();
  });
});
