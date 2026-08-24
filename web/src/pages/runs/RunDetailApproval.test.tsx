// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Routes, Route } from "react-router-dom";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
  Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
}));
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
vi.mock("../../auth", () => ({ useAuth: () => ({ token: "tok", hasPerm: () => true, me: {} }) }));

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
    listDrops: vi.fn().mockResolvedValue({ drops: [] }),
  },
}));

import { RunDetail } from "./RunDetail";

function renderRun() {
  return render(
    <MemoryRouter initialEntries={["/runs/run-1"]}>
      <Routes>
        <Route path="/runs/:runID" element={<RunDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

// The run page could only STOP a run, never resolve one — so the "approval
// needed" email had nowhere useful to send a recipient without a signed
// one-click link. This pins the control being present.
describe("RunDetail approvals", () => {
  it("offers approve/reject for a node the run is parked on", async () => {
    getJob.mockResolvedValue({ ID: "run-1", GraphID: "refunds", Status: "awaiting" });
    listRunNodes.mockResolvedValue({
      nodes: [{
        NodeID: "gate", Status: "awaiting",
        Result: { status: "awaiting", output: { prompt: { data: "Refund 230 kr?" } } },
      }],
    });
    renderRun();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "common.approve" })).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: "inspector.reject" })).toBeInTheDocument();
    expect(screen.getByText("Refund 230 kr?")).toBeInTheDocument();
  });

  it("offers nothing to approve on a finished run", async () => {
    getJob.mockResolvedValue({ ID: "run-1", GraphID: "refunds", Status: "succeeded" });
    listRunNodes.mockResolvedValue({
      nodes: [{ NodeID: "gate", Status: "succeeded", Result: { status: "ok" } }],
    });
    renderRun();
    await waitFor(() => expect(getJob).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "common.approve" })).toBeNull();
  });
});
