// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// A skipped step in the timeline said only "Skipped", and opening the row said
// "no result recorded" — which is true and useless. The row now names the
// reason, and the open row explains it in a sentence.

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Routes, Route } from "react-router-dom";

vi.mock("react-i18next", () => {
  const catalog: Record<string, string> = {
    "skip.notFired": "Didn't fire",
    "skip.notFiredHint":
      "This trigger didn't start the run — a different trigger did, so the steps after it were left alone.",
    "skip.upstream": "Not reached",
    "skip.upstreamHint": "A step before this one was skipped, failed, or sent the flow down another branch.",
    "skip.stepOff": "Switched off",
    "skip.stepOffHint": "This step is switched off, so the run passed over it.",
    "skip.skipped": "Skipped",
    "skip.skippedHint": "This step didn't run.",
    "runDetail.noResult": "No result recorded",
    "runDetail.timeline": "Timeline",
  };
  const t = (k: string) => catalog[k] ?? k;
  return {
    useTranslation: () => ({ t }),
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok",
    hasPerm: () => true,
    me: { tenant: "acme", workspace: "main" },
    activeTenant: "acme",
    activeWorkspace: "main",
  }),
}));

const getJob = vi.fn();
const listRunNodes = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    getJob: (...a: unknown[]) => getJob(...a),
    listRunNodes: (...a: unknown[]) => listRunNodes(...a),
    approveNode: vi.fn().mockResolvedValue({}),
    listRunLogs: vi.fn().mockResolvedValue({ entries: [] }),
    getGraph: vi.fn().mockResolvedValue(null),
    loadGraph: vi.fn().mockResolvedValue(null),
    listDrops: vi.fn().mockResolvedValue({ drops: [] }),
    downloadWorkspaceFile: vi.fn(),
  },
}));

import { RunDetail } from "./RunDetail";

const RUN_ID = "69a6f59b21aa3a4e7530df27";

function renderRun(nodes: unknown[]) {
  getJob.mockResolvedValue({ ID: RUN_ID, GraphID: "relay", Status: "succeeded" });
  listRunNodes.mockResolvedValue({ nodes });
  return render(
    <MemoryRouter initialEntries={[`/runs/${RUN_ID}`]}>
      <Routes>
        <Route path="/runs/:runID" element={<RunDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

const skippedNode = (nodeID: string, skipCode?: string) => ({
  ID: "j-" + nodeID,
  NodeID: nodeID,
  Status: "skipped",
  Result: skipCode ? { skip_code: skipCode } : {},
});

describe("a skipped step in the run timeline", () => {
  it("names the reason on the row", async () => {
    renderRun([skippedNode("hook", "trigger_not_fired"), skippedNode("notify", "upstream")]);
    await waitFor(() => expect(screen.getByText("Didn't fire")).toBeTruthy());
    expect(screen.getByText("Not reached")).toBeTruthy();
  });

  it("explains it in the open row, instead of claiming there is nothing to show", async () => {
    renderRun([skippedNode("hook", "trigger_not_fired")]);
    await waitFor(() => expect(screen.getByText("Didn't fire")).toBeTruthy());
    await userEvent.click(screen.getByText("hook"));
    expect(await screen.findByText(/a different trigger did/)).toBeTruthy();
    expect(screen.queryByText("No result recorded")).toBeNull();
  });

  // Unlike the canvas card, this page has no "Off" chip of its own, so the
  // switched-off step has to be explained here too.
  it("explains a switched-off step", async () => {
    renderRun([skippedNode("quiet", "step_off")]);
    await waitFor(() => expect(screen.getByText("Switched off")).toBeTruthy());
  });

  // A run recorded before skip codes existed, or by a newer daemon with a code
  // this build has no copy for: still labelled, never left bare.
  it("falls back to a plain label when the record carries no code", async () => {
    renderRun([skippedNode("legacy")]);
    // The chip itself, not the status word beside it.
    await waitFor(() =>
      expect(document.querySelector(".node-skip")?.textContent).toBe("Skipped"),
    );
  });
});
