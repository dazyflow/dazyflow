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
    // The rest of the editor's api surface. Stubbed rather than omitted so a
    // mount doesn't die in an unrelated effect — the editor calls 24 methods
    // and only the ones above matter to the run lifecycle.
    listProviders: () => Promise.resolve({ providers: [] }),
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
    // While a run is in flight the same toolbar slot becomes Stop.
    expect(await screen.findByText("runAction.stop")).toBeInTheDocument();
    expect(screen.queryByText("editor.run")).not.toBeInTheDocument();
  });

  it("reports success with the finishing step's label once the run terminates", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await emit(...frame.node("manual_1", "succeeded"));
    await emit(...frame.node("ntfy_1", "succeeded"));
    await emit(...frame.terminal("succeeded"));

    // The success toast is the only signal that a run worked; a border tint on
    // each node reads as "nothing happened". Match the headline, not the
    // "view the run" link that sits under it.
    expect(await screen.findByText(succeededHeadline)).toBeInTheDocument();
  });

  it("raises the failure banner naming the step that failed", async () => {
    getNodeRecord.mockResolvedValue({
      Result: { error: { message: "no topic configured" } },
    });
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await emit(...frame.node("ntfy_1", "failed"));
    await waitFor(() => expect(getNodeRecord).toHaveBeenCalled());

    expect(await screen.findByText(/editor.runFailed/)).toBeInTheDocument();
  });

  it("offers Retry on the failure banner, and resumes the failed run", async () => {
    getNodeRecord.mockResolvedValue({
      Result: { error: { message: "no topic configured" } },
    });
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await emit(...frame.node("ntfy_1", "failed"));
    await emit(...frame.terminal("failed"));

    const retry = await screen.findByText("runAction.retry");
    await userEvent.click(retry);
    // Resumes THIS run rather than starting a fresh one, and keeps watching on
    // the canvas instead of navigating to the run page.
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

    // The node banner names the step; the terminal handler must stay quiet.
    expect(screen.queryByText(/editor.runFailedGraph/)).not.toBeInTheDocument();
    expect(await screen.findByText(/editor.runFailed/)).toBeInTheDocument();
  });

  it("reports a graph-level failure when no node ever failed", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    // A build/validation error or a global timeout terminates the run without
    // any per-node frame; without this branch the canvas just goes quiet.
    await emit(...frame.terminal("failed", { message: "timeout" }));

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

  it("stops an in-flight run through cancelRun", async () => {
    mount();
    await userEvent.click(await screen.findByText("editor.run"));
    await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));

    await userEvent.click(await screen.findByText("runAction.stop"));
    // The third argument is the reason the daemon records against the run.
    await waitFor(() =>
      expect(cancelRun).toHaveBeenCalledWith("tok", "run-1", expect.any(String)),
    );
  });
});
