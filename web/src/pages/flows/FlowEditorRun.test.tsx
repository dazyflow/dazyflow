// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The editor's run lifecycle: pressing Run, watching frames arrive over the
// stream, and what the canvas says when the run ends.
//
// This is the cluster most worth pinning before it becomes a hook. Its state is
// spread over runOutputs / runDone / failedRun / liveLogs / running /
// currentRunID / lockedRunID / pausedAt / stepping, written by one SSE handler
// and read by a dozen places, and the interesting cases are orderings — a
// terminal frame overtaking a node-record fetch, a second run superseding the
// first — which is precisely what a refactor breaks and a type checker cannot
// see.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import {
  frame,
  installLayoutStubs,
  makeStreamJob,
  manifests,
  twoStepGraph,
} from "./editorTestHarness";

installLayoutStubs();

vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o.label === "string" ? `${k}:${o.label}` : k;
  const value = { t, i18n: { language: "en" } };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({
  default: { language: "en", t: (k: string) => k },
}));
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
const runGraph = vi.fn();
const listRuns = vi.fn();
const getNodeRecord = vi.fn();
const retryRun = vi.fn();
const cancelRun = vi.fn();

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
    loadGraph: (...a: unknown[]) => Promise.resolve(twoStepGraph(String(a[3]))),
    listDrops: () => Promise.resolve({ drops: manifests }),
    dropSuggestions: () => Promise.resolve([]),
    listSecrets: () => Promise.resolve({ secrets: [] }),
    getPublishedInfo: () => Promise.resolve({ published: false }),
    flowHistory: () => Promise.resolve({ revisions: [] }),
    saveGraph: () => Promise.resolve({}),
    streamJob: (...a: Parameters<typeof stream.streamJob>) => stream.streamJob(...a),
    runGraph: (...a: unknown[]) => runGraph(...a),
    listRuns: (...a: unknown[]) => listRuns(...a),
    getNodeRecord: (...a: unknown[]) => getNodeRecord(...a),
    retryRun: (...a: unknown[]) => retryRun(...a),
    cancelRun: (...a: unknown[]) => cancelRun(...a),
    sampleNode: () => Promise.resolve({}),
    listProviders: () => Promise.resolve({ providers: [] }),
    listSSHCredentials: () => Promise.resolve({ credentials: [] }),
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

function mount(id = "coffee-reorder", search = "") {
  return render(
    <MemoryRouter initialEntries={[`/flows/${id}${search}`]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
      </Routes>
    </MemoryRouter>,
  );
}

// Drive a frame through the editor's current subscription, wrapped in act so
// the resulting state updates flush before the assertion.
async function emit(kind: string, data: unknown) {
  const sub = stream.latest();
  expect(sub, "no open run stream").toBeTruthy();
  await act(async () => {
    sub.emit(kind, data);
  });
}

// The toast's headline is either editor.runSucceeded or
// editor.runSucceededWith:<label> depending on whether the finishing step had a
// resolvable label; both must be told apart from editor.runSucceededDetails.
const succeededHeadline = (text: string) =>
  text.startsWith("editor.runSucceeded") && !text.includes("Details");

beforeEach(() => {
  stream.subs.length = 0;
  runGraph.mockResolvedValue({ job_id: "run-1" });
  listRuns.mockResolvedValue({ runs: [] });
  getNodeRecord.mockResolvedValue({ Result: { output: {} } });
  retryRun.mockResolvedValue({ job_id: "run-2" });
  cancelRun.mockResolvedValue({});
});

async function openErrors() {
  await userEvent.click(
    await screen.findByRole("button", { name: /editor.issuesErrorsTitle/ }),
  );
}

describe("editor run lifecycle", () => {
  it("mounts the flow and offers Run", async () => {
    mount();
    expect(await screen.findByText("editor.run")).toBeInTheDocument();
  });

  it("opens a stream for the job Run returns, and swaps Run for Stop", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(runGraph).toHaveBeenCalled());
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));
    expect(await screen.findByText("runAction.stop")).toBeInTheDocument();
    expect(screen.queryByText("editor.run")).not.toBeInTheDocument();
  });

  // Where the outcome lands. It has moved twice, and both moves were for the
  // same reason: announcing a success must not cost the author sight of the
  // flow. Out of the docked banner strip first (it grows to 40vh, so it
  // resized the editor), then out of the canvas overlay it went to — pinned at
  // top-centre, which is exactly where a flow begins. It now sits in the
  // toolbar, which neither resizes the canvas nor covers it.
  it("puts the outcome in the toolbar, not over the flow or in the docked strip", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));
    await emit(...frame.terminal("succeeded"));

    await screen.findByText(succeededHeadline);
    const status = document.querySelector(".editor-run-status");
    if (!status) throw new Error("success status not rendered");
    expect(status.closest(".editor-banner-stack")).toBeNull();
    expect(status.closest(".editor-toolbar")).not.toBeNull();
    expect(status.closest(".toolbar-scroll")).toBeNull();
  });

  it("reports success with the finishing step's label once the run terminates", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await emit(...frame.node("manual_1", "succeeded"));
    await emit(...frame.node("ntfy_1", "succeeded"));
    await emit(...frame.terminal("succeeded"));

    expect(await screen.findByText(succeededHeadline)).toBeInTheDocument();
  });

  it("reports the failure in the Errors panel, naming the step that failed", async () => {
    getNodeRecord.mockResolvedValue({
      Result: { error: { message: "no topic configured" } },
    });
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await emit(...frame.node("ntfy_1", "failed"));
    await waitFor(() => expect(getNodeRecord).toHaveBeenCalled());

    await openErrors();
    expect(await screen.findByText(/editor.runFailed/)).toBeInTheDocument();
  });

  it("offers Retry in the Errors panel, and resumes the failed run", async () => {
    getNodeRecord.mockResolvedValue({
      Result: { error: { message: "no topic configured" } },
    });
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await emit(...frame.node("ntfy_1", "failed"));
    await emit(...frame.terminal("failed"));

    await openErrors();
    const retry = await screen.findByText("runAction.retry");
    await userEvent.click(retry);
    await waitFor(() => expect(retryRun).toHaveBeenCalledWith("tok", "run-1"));
    expect(runGraph).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-2"));
  });

  it("does not report a graph-level failure twice when a node already did", async () => {
    getNodeRecord.mockResolvedValue({
      Result: { error: { message: "no topic configured" } },
    });
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await emit(...frame.node("ntfy_1", "failed"));
    await emit(...frame.terminal("failed", { message: "graph failed" }));

    // The node's message names the step; the terminal handler must stay quiet.
    await openErrors();
    expect(screen.queryByText(/editor.runFailedGraph/)).not.toBeInTheDocument();
    expect(await screen.findByText(/editor.runFailed/)).toBeInTheDocument();
  });

  it("reports a graph-level failure when no node ever failed", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await emit(...frame.terminal("failed", { message: "timeout" }));

    await openErrors();
    expect(
      await screen.findByText(/editor.runFailedGraph/),
    ).toBeInTheDocument();
  });

  it("aborts the previous stream when a second run starts", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));
    const first = stream.latest();

    await emit(...frame.terminal("succeeded"));
    runGraph.mockResolvedValue({ job_id: "run-3" });
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-3"));

    // Two readers writing the canvas at once would interleave node statuses
    // from different runs.
    expect(first.aborted()).toBe(true);
  });

  it("clears the previous run's success toast when a new run starts", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));
    await emit(...frame.terminal("succeeded"));
    expect(await screen.findByText(succeededHeadline)).toBeInTheDocument();

    runGraph.mockResolvedValue({ job_id: "run-4" });
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-4"));

    // A stale "it worked" next to a running flow is worse than no banner.
    await waitFor(() =>
      expect(screen.queryByText(succeededHeadline)).not.toBeInTheDocument(),
    );
  });

  // Which run, if any, a freshly opened editor attaches to. Opening a flow to
  // work on it must show the graph, not last week's node statuses painted over
  // it: a green border means "this step just ran", and on a flow nobody has
  // touched today it is a lie the canvas tells about itself.
  describe("attaching on open", () => {
    it("opens no run stream when the flow is opened on its own", async () => {
      listRuns.mockResolvedValue({
        runs: [{ id: "run-old", status: "succeeded" }],
      });
      // Earlier builds stuck the last run id here and replayed it on open;
      // browsers that ran those builds still carry the key, and it must stay
      // inert rather than colour the canvas again.
      localStorage.setItem("dazyflow.lastRun.coffee-reorder", "run-old");
      mount();
      expect(await screen.findByText("editor.run")).toBeInTheDocument();
      await waitFor(() => expect(listRuns).toHaveBeenCalled());
      expect(stream.subs.length).toBe(0);
    });

    it("attaches to the run named by ?run=, the link the run pages hand over", async () => {
      mount("coffee-reorder", "?run=run-7");
      await waitFor(() => expect(stream.latest()?.runID).toBe("run-7"));
    });

    // Reloading mid-run drops the run id from nowhere else to recover it, and
    // an editor locked by a run it is not watching shows no progress at all.
    it("attaches to a run still in flight, found through the lock", async () => {
      listRuns.mockResolvedValue({
        runs: [{ id: "run-live", status: "running" }],
      });
      mount();
      await waitFor(() => expect(stream.latest()?.runID).toBe("run-live"));
      expect(await screen.findByText("runAction.stop")).toBeInTheDocument();
    });

    it("subscribes once when ?run= and the lock name the same run", async () => {
      listRuns.mockResolvedValue({
        runs: [{ id: "run-7", status: "running" }],
      });
      mount("coffee-reorder", "?run=run-7");
      await waitFor(() => expect(stream.latest()?.runID).toBe("run-7"));
      await waitFor(() => expect(listRuns).toHaveBeenCalled());
      // Two readers of one run interleave their writes to the canvas.
      expect(stream.subs.length).toBe(1);
    });
  });

  it("stops an in-flight run through cancelRun", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await userEvent.click(await screen.findByText("runAction.stop"));
    await waitFor(() =>
      expect(cancelRun).toHaveBeenCalledWith("tok", "run-1", expect.any(String)),
    );
  });
});

// A skipped step used to look exactly like one the run had not reached yet:
// same card, no chip, nothing said. The reason travels with the status frame.
describe("a skipped step on the canvas", () => {
  const skipChip = () => document.querySelector(".dz-node-skipchip");

  async function runTo(nodeID: string, skipCode: string) {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));
    await emit(...frame.skipped(nodeID, skipCode));
  }

  it("says a trigger did not fire", async () => {
    await runTo("ntfy_1", "trigger_not_fired");
    await waitFor(() => expect(skipChip()).toBeTruthy());
    expect(skipChip()?.textContent).toContain("skip.notFired");
  });

  it("distinguishes a step the flow never reached", async () => {
    await runTo("ntfy_1", "upstream");
    await waitFor(() => expect(skipChip()).toBeTruthy());
    expect(skipChip()?.textContent).toContain("skip.upstream");
  });

  // The card already carries an "Off" chip for a switched-off step, and two
  // chips saying the same thing is worse than one.
  it("leaves a switched-off step to its own chip", async () => {
    await runTo("ntfy_1", "step_off");
    await waitFor(() => expect(stream.latest()).toBeTruthy());
    expect(skipChip()).toBeNull();
  });

  // Starting a second run must clear the first one's verdicts, or a card keeps
  // explaining a skip that belongs to a finished run.
  it("clears the chip when the next run starts", async () => {
    await runTo("ntfy_1", "upstream");
    await waitFor(() => expect(skipChip()).toBeTruthy());
    await emit(...frame.terminal("succeeded"));

    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(skipChip()).toBeNull());
  });
});
