// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// A freshly opened editor knows what its steps last produced.
//
// The card data faces read per-node outputs, and until now those existed only
// in memory for the length of a session: reload and every face went back to
// "no data yet". The samples fetch answers the same question from the node
// records the runs already wrote, and the editor reads it UNDER the live run
// values so a running flow always wins.

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { installLayoutStubs, makeStreamJob, manifests, twoStepGraph } from "./editorTestHarness";

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
const flowSamples = vi.fn();

vi.mock("../../api", () => ({
  APIError: class extends Error {},
  isHTTPStatus: () => false,
  isErrorCode: () => false,
  api: {
    loadGraph: (...a: unknown[]) => Promise.resolve(twoStepGraph(String(a[3]))),
    listDrops: () => Promise.resolve({ drops: manifests }),
    dropSuggestions: () => Promise.resolve([]),
    listSecrets: () => Promise.resolve({ secrets: [] }),
    getPublishedInfo: () => Promise.resolve({ published: false }),
    flowHistory: () => Promise.resolve({ revisions: [] }),
    saveGraph: () => Promise.resolve({}),
    streamJob: (...a: Parameters<typeof stream.streamJob>) => stream.streamJob(...a),
    listRuns: () => Promise.resolve({ runs: [] }),
    flowSamples: (...a: unknown[]) => flowSamples(...a),
    // The rest of the editor's api surface. Stubbed rather than omitted
    // because a mount dies in an unrelated effect without them (see
    // FlowEditorRun.test.tsx) — and a dead mount renders no cards, so every
    // assertion here would fail for the wrong reason.
    runGraph: () => Promise.resolve({ job_id: "r1" }),
    getNodeRecord: () => Promise.resolve({}),
    retryRun: () => Promise.resolve({}),
    cancelRun: () => Promise.resolve({}),
    sampleNode: () => Promise.resolve({}),
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
}));

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

describe("a step's last output on a freshly opened editor", () => {
  it("shows what the step produced on record, with nothing run this session", async () => {
    flowSamples.mockResolvedValue({
      flow: "coffee-reorder",
      nodes: { ntfy_1: { out: { mime: "text/plain", data: "Ordered 2kg of beans" } } },
    });
    const user = userEvent.setup();
    mount();
    await waitFor(() => expect(flowSamples).toHaveBeenCalled());
    // It is asked for by flow, not by run — there is no run id to hand.
    expect(flowSamples).toHaveBeenCalledWith("tok", "acme", "main", "coffee-reorder");

    await user.click(await screen.findByRole("button", { name: "editor.dataView" }));
    // Scoped to the data face: the same value also reaches the port hover-peek,
    // which reads the same per-node outputs and so gains the same persistence.
    // A short text value is its own shape line, so the card shows it verbatim
    // rather than describing it.
    await waitFor(() =>
      expect(document.querySelector(".dz-face-line-value")?.textContent).toContain(
        "Ordered 2kg of beans",
      ),
    );
  });

  it("leaves the faces empty when retention has taken the records", async () => {
    flowSamples.mockResolvedValue({ flow: "coffee-reorder", nodes: {} });
    const user = userEvent.setup();
    mount();
    await waitFor(() => expect(flowSamples).toHaveBeenCalled());

    await user.click(await screen.findByRole("button", { name: "editor.dataView" }));
    // "No data yet" rather than a blank panel — the card says so itself,
    // on every step of the flow.
    expect(await screen.findAllByText("nodeCard.face.noData")).not.toHaveLength(0);
  });

  it("survives the endpoint failing, since a card with no sample says so", async () => {
    flowSamples.mockRejectedValue(new Error("boom"));
    const user = userEvent.setup();
    mount();
    await waitFor(() => expect(flowSamples).toHaveBeenCalled());

    // The canvas still mounts and folds; nothing is reported to the user.
    await user.click(await screen.findByRole("button", { name: "editor.dataView" }));
    expect(await screen.findAllByText("nodeCard.face.noData")).not.toHaveLength(0);
  });
});
