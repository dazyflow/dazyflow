// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Routes, Route } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o ? `${k} ${JSON.stringify(o)}` : k;
  return {
    useTranslation: () => ({ t }),
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
// One stable object: the real context memoizes its value, and a fresh literal
// per call would make every effect that depends on it re-fire endlessly.
const AUTH = {
  token: "tok",
  hasPerm: () => true,
  me: { tenant: "acme", workspace: "ws1" },
  activeTenant: "acme",
  activeWorkspace: "ws1",
};
vi.mock("../../auth", () => ({ useAuth: () => AUTH }));

const getJob = vi.fn();
const listRunNodes = vi.fn();
const replayRun = vi.fn();
const runGraph = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    getJob: (...a: unknown[]) => getJob(...a),
    listRunNodes: (...a: unknown[]) => listRunNodes(...a),
    replayRun: (...a: unknown[]) => replayRun(...a),
    runGraph: (...a: unknown[]) => runGraph(...a),
    retryRun: vi.fn(),
    cancelRun: vi.fn(),
    loadGraph: vi.fn().mockResolvedValue({ id: "refunds", name: "Refunds", nodes: [], edges: [] }),
    listDrops: vi.fn().mockResolvedValue({ drops: [] }),
    listRunLogs: vi.fn().mockResolvedValue({ entries: [] }),
  },
}));

import { RunDetail } from "./RunDetail";

const RUN_ID = "69a6f59b21aa3a4e7530df27";

function renderRun() {
  return render(
    <MemoryRouter initialEntries={[`/runs/${RUN_ID}`]}>
      <Routes>
        <Route path="/runs/:runID" element={<RunDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

// Confirm the replay, which is gated behind a dialog because it re-fires side
// effects. Both the header button and the dialog's confirm button carry the
// same label, so the dialog's is picked out of the dialog itself.
async function pressReplay() {
  const user = userEvent.setup();
  await user.click(
    (await screen.findAllByRole("button", { name: /runAction\.replay/ }))[0],
  );
  const dialog = await screen.findByRole("alertdialog");
  await user.click(within(dialog).getByRole("button", { name: /runAction\.replay/ }));
}

// A flow started by a webhook or a hosted form begins at a step whose data
// arrived with that request. Replay used to re-submit the FLOW (runGraph),
// which left that step with nothing, so the whole re-run died on its first
// step with "nothing was sent to this flow". The run's own replay endpoint
// re-sends the delivery the original run received.
describe("RunDetail replay", () => {
  beforeEach(() => {
    getJob.mockResolvedValue({
      ID: RUN_ID,
      GraphID: "refunds",
      Status: "succeeded",
      Tenant: "acme",
      Workspace: "ws1",
    });
    listRunNodes.mockResolvedValue({ nodes: [] });
    replayRun.mockReset();
    runGraph.mockReset();
  });

  it("replays the run itself, so webhook data is re-sent", async () => {
    replayRun.mockResolvedValue({ job_id: "run-2" });
    renderRun();
    await screen.findByRole("heading", { level: 1 });

    await pressReplay();

    await waitFor(() => expect(replayRun).toHaveBeenCalledWith("tok", RUN_ID));
    // Re-submitting the flow is exactly the behaviour that dropped the
    // trigger data — it must not be how Replay works any more.
    expect(runGraph).not.toHaveBeenCalled();
  });

  it("explains a refusal instead of leaving the button spinning", async () => {
    class APIErrorStub extends Error {}
    replayRun.mockRejectedValue(new APIErrorStub("no delivery"));
    renderRun();
    await screen.findByRole("heading", { level: 1 });

    await pressReplay();

    await waitFor(() =>
      expect(screen.getByText(/runDetail\.replayFailed/)).toBeInTheDocument(),
    );
    // The button is usable again — a failed replay must not strand it in
    // "Replaying…".
    expect(
      screen.getAllByRole("button", { name: /runAction\.replay/ }).length,
    ).toBeGreaterThan(0);
  });
});
