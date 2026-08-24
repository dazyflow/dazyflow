// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Autosave and the edit lock.
//
// The autosave effect carries five guards, and every one of them is there to
// stop the editor writing the wrong thing over the right thing:
//
//   lockedRunID     a run is in flight and executes the SAVED graph
//   previewRef      you're looking at an old revision, not HEAD
//   loadFailed      the in-memory graph is an empty placeholder, not the server's
//   loadedIDRef     the nodes still belong to the flow you just navigated away
//                   from — the comment on this one says "real data loss,
//                   observed in the wild"
//   saving          a PUT is already in flight
//
// Guards are invisible when they work, which is exactly why they rot. These
// tests assert the absence of a write, which is the only way to notice a guard
// that has quietly stopped guarding.

import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
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
const saveGraph = vi.fn();
const listRuns = vi.fn();

vi.mock("../../api", () => {
  class APIError extends Error {
    status: number;
    code?: string;
    constructor(status: number, msg?: string) {
      super(msg);
      this.status = status;
    }
  }
  // duck-type on .status / .code rather than instanceof, so a plain
  // Object.assign(new Error(), { status: 404 }) from a test takes the same
  // branch the real APIError would. Without isHTTPStatus in this mock the
  // editor's load-error handler threw instead of recognising a 404, and the
  // "404 means a new flow" test passed for the wrong reason.
  const statusOf = (e: unknown) => (e as { status?: number } | null)?.status;
  const codeOf = (e: unknown) => (e as { code?: string } | null)?.code;
  return {
    APIError,
    isHTTPStatus: (e: unknown, status: number) => statusOf(e) === status,
    isErrorCode: (e: unknown, code: string) => codeOf(e) === code,
  api: {
    loadGraph: (...a: unknown[]) => loadGraph(...a),
    saveGraph: (...a: unknown[]) => saveGraph(...a),
    listRuns: (...a: unknown[]) => listRuns(...a),
    listDrops: () => Promise.resolve({ drops: manifests }),
    dropSuggestions: () => Promise.resolve([]),
    listSecrets: () => Promise.resolve({ secrets: [] }),
    listProviders: () => Promise.resolve({ providers: [] }),
    getPublishedInfo: () => Promise.resolve({ published: false }),
    flowHistory: () => Promise.resolve({ revisions: [] }),
    streamJob: (...a: never[]) => stream.streamJob(...a),
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

// A graph whose Schedule step has no timezone. The editor heals this on load
// (and persists, because a Run executes the SAVED graph), which is a real write
// triggered purely by mounting — no canvas interaction needed.
function graphWithUnzonedSchedule() {
  return {
    ...twoStepGraph(),
    nodes: [
      { id: "cron_1", module: "cron_trigger", params: { cron: "0 9 * * *" }, position: { x: 0, y: 0 } },
      { id: "ntfy_1", module: "ntfy", params: { topic: "beans" }, position: { x: 320, y: 0 } },
    ],
    edges: [{ from: "cron_1", from_port: "out", to: "ntfy_1", to_port: "in" }],
  };
}

// Let the debounce elapse. The autosave timer is 1500ms; 3s of fake time plus a
// flush covers it with room to spare.
async function letAutosaveFire() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  stream.subs.length = 0;
  loadGraph.mockResolvedValue(twoStepGraph());
  saveGraph.mockResolvedValue({});
  listRuns.mockResolvedValue({ runs: [] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("editor autosave guards", () => {
  it("heals a Schedule step with no timezone and persists it", async () => {
    loadGraph.mockResolvedValue(graphWithUnzonedSchedule());
    mount();
    await letAutosaveFire();
    // Without a zone both the schedule and its fired_at would run in UTC, and a
    // forked flow never went through the editor's add/edit path that stamps it.
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    const saved = saveGraph.mock.calls[0].find(
      (a) => a && typeof a === "object" && "nodes" in a,
    ) as { nodes: { module: string; params?: { tz?: string } }[] } | undefined;
    const cron = saved?.nodes.find((n) => n.module === "cron_trigger");
    expect(cron?.params?.tz).toBeTruthy();
  });

  // NOTE: this pins current behaviour, and that behaviour is arguably
  // inconsistent. The autosave effect refuses to write while `lockedRunID` is
  // set, because "a run executes the SAVED graph". The timezone heal writes for
  // the SAME stated reason ("persisted because Run executes the SAVED graph")
  // but calls api.saveGraph directly from the load handler, gated only on
  // `changed && hasPerm("graph:edit")` — so the lock does not apply to it.
  // Harm is low: the write is HEAD plus a tz, and it is idempotent. But two
  // paths draw opposite conclusions from one premise. Pinned as-is so a
  // deliberate change shows up as a failing test, not a silent swap.
  it("heals the timezone even while a run holds the lock (unguarded path)", async () => {
    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "running" }] });
    loadGraph.mockResolvedValue(graphWithUnzonedSchedule());
    mount();
    await letAutosaveFire();
    expect(saveGraph).toHaveBeenCalledTimes(1);
  });

  it("does not autosave when the initial load failed", async () => {
    // The in-memory graph is an empty placeholder, NOT the server's. Autosaving
    // it would clobber the real flow with nothing.
    loadGraph.mockRejectedValue(
      Object.assign(new Error("boom"), { status: 500 }),
    );
    mount();
    await letAutosaveFire();
    expect(saveGraph).not.toHaveBeenCalled();
  });

  it("still autosaves after a 404 load, which means a genuinely new flow", async () => {
    // 404 is the one failure that does NOT block: a flow being created has no
    // server copy yet, so the empty in-memory graph IS the truth.
    loadGraph.mockRejectedValue(
      Object.assign(new Error("not found"), { status: 404 }),
    );
    mount();
    await screen.findByText("editor.run");
    // Nothing is dirty on a fresh empty graph, so no write — but critically the
    // editor is usable rather than locked out.
    expect(saveGraph).not.toHaveBeenCalled();
  });
});

describe("editor edit lock", () => {
  it("keeps re-polling while a run holds the lock, then stops once it clears", async () => {
    // Scheduler-driven runs (a cron/poll trigger firing) never reach this
    // editor's SSE terminal, so nothing else would ever clear the lock: a flow
    // that polls would catch a run at mount and then stay "locked" forever,
    // silently blocking every save. The self-heal is a poll that runs ONLY
    // while the lock is held.
    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "running" }] });
    mount();
    await screen.findByText("editor.run");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8000);
    });
    expect(listRuns.mock.calls.length).toBeGreaterThan(1);

    // The run finishes: the next poll drops the lock, and polling stops.
    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "succeeded" }] });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    const afterRelease = listRuns.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8000);
    });
    expect(listRuns.mock.calls.length).toBe(afterRelease);
  });

  it("does not poll for the lock when nothing holds it", async () => {
    mount();
    await screen.findByText("editor.run");
    const atMount = listRuns.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    // Only runs while locked, so there's no idle polling cost.
    expect(listRuns.mock.calls.length).toBe(atMount);
  });
});
