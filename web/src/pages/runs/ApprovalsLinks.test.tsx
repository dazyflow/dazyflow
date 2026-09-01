// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

// Stable `t` / useTranslation result: Approvals' refresh callback lists `t` in
// its deps, so a fresh function per render re-fires it forever.
vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../auth", () => {
  const auth = {
    token: "tok",
    me: { tenant: "t", workspace: "ws" },
    activeTenant: "t",
    activeWorkspace: "ws",
    hasPerm: () => true,
  };
  return { useAuth: () => auth };
});

const listPendingApprovals = vi.fn();
const listDecidedApprovals = vi.fn();
const listGraphs = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listPendingApprovals: (...a: unknown[]) => listPendingApprovals(...a),
    listDecidedApprovals: (...a: unknown[]) => listDecidedApprovals(...a),
    listGraphs: (...a: unknown[]) => listGraphs(...a),
    approveNode: vi.fn().mockResolvedValue({}),
  },
}));

import { Approvals } from "./Approvals";

const RUN_ID = "69a6f59b21aa3a4e7530df27";
const FLOW_ID = "refunds";

// The inbox has to lead to the run. Its only link used to open the EDITOR,
// which is the one surface that cannot service the decision: `canEdit` is gated
// on `lockedRunID`, so a parked run makes the editor read-only, and the
// deliberate approve/reject control was deliberately removed from the Inspector
// because the editor is graph-scoped and could resolve the wrong run (see
// ApprovalPanel's header). The run page carries that panel plus the timeline of
// steps that already ran — the evidence the approver is deciding on. Same
// correction, and same reasoning, as RunListLinks.test.tsx pins for the runs
// list.
describe("Approvals card links", () => {
  beforeEach(() => {
    listGraphs.mockResolvedValue({ graphs: [{ id: FLOW_ID, name: "Refunds" }] });
    // No history here: these cases count the links on the page, and a decided
    // row carries one of its own.
    listDecidedApprovals.mockResolvedValue({ approvals: [] });
    listPendingApprovals.mockResolvedValue({
      approvals: [
        {
          run_id: RUN_ID,
          node_id: "approve_1",
          graph_id: FLOW_ID,
          prompt: "Refund 240 SEK to alice@example.com?",
          since: "2026-08-24T09:00:00Z",
        },
      ],
    });
  });

  it("points the flow-name link at the run, not the editor", async () => {
    render(<MemoryRouter><Approvals /></MemoryRouter>);
    const name = await screen.findByRole("link", { name: /Refunds/ });
    expect(name).toHaveAttribute("href", `/runs/${RUN_ID}`);
  });

  it("keeps the editor reachable as the card's secondary action", async () => {
    render(<MemoryRouter><Approvals /></MemoryRouter>);
    await waitFor(() => expect(listPendingApprovals).toHaveBeenCalled());
    const edit = await screen.findByRole("link", {
      name: "common.openInEditor",
    });
    expect(edit).toHaveAttribute("href", `/flows/${FLOW_ID}?run=${RUN_ID}`);
  });

  it("offers exactly one run link and one editor link per card", async () => {
    render(<MemoryRouter><Approvals /></MemoryRouter>);
    await screen.findByRole("link", { name: /Refunds/ });
    const hrefs = screen
      .getAllByRole("link")
      .map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual([`/runs/${RUN_ID}`, `/flows/${FLOW_ID}?run=${RUN_ID}`]);
  });
});
