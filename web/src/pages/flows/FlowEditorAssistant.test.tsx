// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Watching an assistant build the flow: its saves arrive on the watch stream
// and the canvas shows them (ticker, lit nodes); ?watch=1 is the phone view;
// and the canvas reports what it shows so "this step" means something.

import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { render, waitFor, act, screen } from "@testing-library/react";
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
const putCanvasFocus = vi.fn((..._a: unknown[]) => Promise.resolve());

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
      watchFlow: (...a: unknown[]) => watchFlow(...a),
      putCanvasFocus: (...a: unknown[]) => putCanvasFocus(...a),
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

function mount(url = "/flows/coffee-reorder") {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
      </Routes>
    </MemoryRouter>,
  );
}

const withAddedStep = () => {
  const g = twoStepGraph();
  g.nodes.push({ id: "ntfy_2", module: "ntfy", params: { topic: "milk" }, position: { x: 640, y: 0 } });
  return g;
};

describe("assistant live view", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    loadGraph.mockReset();
    loadGraph.mockResolvedValueOnce(twoStepGraph()).mockResolvedValue(withAddedStep());
    putCanvasFocus.mockClear();
    watchFlow.mockReset();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("shows an assistant's save as it lands, with its note", async () => {
    let push: (ev: unknown) => void = () => {};
    watchFlow.mockImplementation((_t, _te, _w, _id, onUpdated: (ev: unknown) => void) => {
      push = onUpdated;
      return new Promise(() => {});
    });
    mount();
    await waitFor(() => expect(watchFlow).toHaveBeenCalled());
    act(() =>
      push({ flow_id: "acme/main/coffee-reorder", commit: "c9", author: "a@b.c", autosave: false,
        assistant: true, note: "Also tell the milk topic", touched: ["ntfy_2"] }),
    );
    expect(await screen.findByText("Also tell the milk topic")).toBeTruthy();
    expect(screen.getByText("editor.assistant.added")).toBeTruthy();
    expect(loadGraph).toHaveBeenCalledTimes(2);
  });

  it("reports the open flow and selection, and watch=1 is the bare read-only canvas", async () => {
    watchFlow.mockImplementation(() => new Promise(() => {}));
    const { container } = mount("/flows/coffee-reorder?watch=1");
    await act(() => vi.advanceTimersByTimeAsync(600));
    await waitFor(() => expect(putCanvasFocus).toHaveBeenCalledWith("tok", "acme/main/coffee-reorder", ""));
    expect(container.querySelector(".editor.editor-watch")).toBeTruthy();
    expect(screen.getByText("editor.watch.openEditor")).toBeTruthy();
  });
});
