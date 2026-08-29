// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The live flow-watch's reconnect.
//
// The watch is an SSE stream the editor reads off a fetch body, and it used to
// be one-shot: watchFlow resolves when the body ends, nothing retried, and the
// effect's deps only cover a flow/auth change. So any drop — a laptop
// sleeping, a network change, a phone backgrounding the tab — left that window
// permanently deaf while its canvas went on looking live. The 25s server ping
// defends against idle proxy timeouts and none of those.
//
// Silence is the whole failure mode, which is what makes these tests worth
// having: nothing about a stalled watch is visible from the outside, so the
// only way it stays fixed is a test that watches for the retry itself.

import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { render, waitFor, act } from "@testing-library/react";
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
const watchFlow = vi.fn();

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
      streamJob: (...a: never[]) => stream.streamJob(...a),
      runGraph: () => Promise.resolve({ job_id: "run-1" }),
      getNodeRecord: () => Promise.resolve({ Result: { output: {} } }),
      retryRun: () => Promise.resolve({ job_id: "run-2" }),
      cancelRun: () => Promise.resolve({}),
      sampleNode: () => Promise.resolve({}),
      watchFlow: (...a: unknown[]) => watchFlow(...a),
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

// A dropped stream: watchFlow's promise settling IS the drop, since it only
// resolves once the response body has ended.
const drops = () => Promise.resolve(undefined);

describe("live flow-watch reconnect", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    loadGraph.mockReset();
    loadGraph.mockResolvedValue(twoStepGraph());
    watchFlow.mockReset();
    watchFlow.mockImplementation(drops);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("resubscribes after the stream drops", async () => {
    mount();
    await waitFor(() => expect(watchFlow).toHaveBeenCalledTimes(1));

    // The first backoff is a second, so nothing should happen before it.
    await act(() => vi.advanceTimersByTimeAsync(400));
    expect(watchFlow).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(watchFlow).toHaveBeenCalledTimes(2);
  });

  it("backs off rather than hammering a server that is not there", async () => {
    mount();
    await waitFor(() => expect(watchFlow).toHaveBeenCalledTimes(1));

    await act(() => vi.advanceTimersByTimeAsync(1100)); // 1s  -> attempt 2
    expect(watchFlow).toHaveBeenCalledTimes(2);

    // The next wait is doubled, so the same elapsed time yields ONE more
    // attempt, not another one per second.
    await act(() => vi.advanceTimersByTimeAsync(1100));
    expect(watchFlow).toHaveBeenCalledTimes(2);

    await act(() => vi.advanceTimersByTimeAsync(1000)); // 2s total -> attempt 3
    expect(watchFlow).toHaveBeenCalledTimes(3);
  });

  it("re-reads the graph on reconnect, because the missed edits are gone", async () => {
    mount();
    await waitFor(() => expect(watchFlow).toHaveBeenCalledTimes(1));
    const loadsBefore = loadGraph.mock.calls.length;

    await act(() => vi.advanceTimersByTimeAsync(1100));

    // A frame carries no graph, so edits that landed while the stream was
    // down were never delivered and nothing will resend them. Only a fetch
    // closes that gap.
    expect(loadGraph.mock.calls.length).toBeGreaterThan(loadsBefore);
  });

  it("stops retrying once the editor goes away", async () => {
    const view = mount();
    await waitFor(() => expect(watchFlow).toHaveBeenCalledTimes(1));

    view.unmount();
    await act(() => vi.advanceTimersByTimeAsync(10000));
    expect(watchFlow).toHaveBeenCalledTimes(1);
  });

  it("retries at once when the tab comes back rather than sitting out the backoff", async () => {
    mount();
    await waitFor(() => expect(watchFlow).toHaveBeenCalledTimes(1));

    // Let the backoff grow, so an immediate retry is distinguishable from
    // simply waiting for the timer.
    await act(() => vi.advanceTimersByTimeAsync(1100)); // attempt 2, next wait 2s
    expect(watchFlow).toHaveBeenCalledTimes(2);

    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    // visibilityState is "visible" in jsdom by default: the phone-unlock case.
    expect(watchFlow).toHaveBeenCalledTimes(3);
  });
});
