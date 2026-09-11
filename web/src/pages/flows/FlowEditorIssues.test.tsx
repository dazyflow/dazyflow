// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The editor's Errors/Warnings buttons, in the editor.
//
// Every message these count used to be a banner stacked over the canvas. The
// two behaviours worth pinning down after the move are the ones a count alone
// would lose: a message the author did not ask for still reaches them, and a
// panel does not outlive the thing it was reporting.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
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


function strayEdgeGraph() {
  const g = twoStepGraph();
  g.edges = [{ from: "manual_1", from_port: "out", to: "ntfy_1", to_port: "nope" }];
  return g;
}

describe("the editor's issue panels", () => {
  beforeEach(() => {
    loadGraph.mockReset();
  });

  it("opens the Warnings panel itself for a message nobody asked for", async () => {
    loadGraph.mockResolvedValue(strayEdgeGraph());
    mount();
    // Not "there is a 1 in the toolbar": the author never clicked anything, so
    // the words have to be on screen. A pruned wire is the editor changing the
    // document under them.
    expect(
      await screen.findByRole("dialog", { name: /editor.issuesWarningsHeading/ }),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(/editor.strayEdgesDropped/),
    ).toBeInTheDocument();
  });

  it("closes the panel when its last row goes away", async () => {
    loadGraph.mockResolvedValue(strayEdgeGraph());
    mount();
    const dialog = await screen.findByRole("dialog", {
      name: /editor.issuesWarningsHeading/,
    });
    expect(dialog).toBeInTheDocument();

    // Dismissing the only warning leaves no button to anchor a panel to, so an
    // empty box must not be left hanging under a control that is gone.
    await userEvent.click(await screen.findByText("common.dismiss"));
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: /editor.issuesWarningsHeading/ }),
      ).not.toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("button", { name: /editor.issuesWarningsTitle/ }),
    ).not.toBeInTheDocument();
  });

  it("says nothing at all about a clean flow", async () => {
    loadGraph.mockResolvedValue(twoStepGraph());
    mount();
    await screen.findAllByRole("button", { name: "editor.tidy" });
    expect(
      screen.queryByRole("button", { name: /editor.issuesWarningsTitle/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /editor.issuesErrorsTitle/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
