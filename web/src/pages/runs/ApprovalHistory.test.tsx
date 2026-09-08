// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

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
const approveNode = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listPendingApprovals: (...a: unknown[]) => listPendingApprovals(...a),
    listDecidedApprovals: (...a: unknown[]) => listDecidedApprovals(...a),
    listGraphs: (...a: unknown[]) => listGraphs(...a),
    approveNode: (...a: unknown[]) => approveNode(...a),
  },
}));

import { Approvals } from "./Approvals";

const RUN_ID = "69a6f59b21aa3a4e7530df27";
const DECIDED_RUN = "7f13c0aa5b2e41d9908ab442";
const FLOW_ID = "refunds";

describe("Approvals history", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listGraphs.mockResolvedValue({ graphs: [{ id: FLOW_ID, name: "Refunds" }] });
    listPendingApprovals.mockResolvedValue({ approvals: [] });
    approveNode.mockResolvedValue({});
    listDecidedApprovals.mockResolvedValue({
      approvals: [
        {
          run_id: DECIDED_RUN,
          node_id: "approve_1",
          graph_id: FLOW_ID,
          prompt: "Refund 240 SEK to alice@example.com?",
          decision: "reject",
          approver: "bo@acme.se",
          comment: "already refunded last week",
          decided_at: "2026-08-24T09:00:00Z",
        },
      ],
    });
  });

  it("shows a settled decision with its note and a link to the run", async () => {
    render(<MemoryRouter><Approvals /></MemoryRouter>);

    await screen.findByText("approvals.historyTitle");
    expect(
      screen.getByText("Refund 240 SEK to alice@example.com?"),
    ).toBeInTheDocument();
    expect(screen.getByText("already refunded last week")).toBeInTheDocument();
    expect(
      screen.getByRole("img", { name: "approvals.historyRejected" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Refunds/ })).toHaveAttribute(
      "href",
      `/runs/${DECIDED_RUN}`,
    );
  });

  it("falls back to the decided value when the step had no prompt", async () => {
    listDecidedApprovals.mockResolvedValue({
      approvals: [
        {
          run_id: DECIDED_RUN,
          node_id: "approve_1",
          graph_id: FLOW_ID,
          decision: "approve",
          approver: "ada@acme.se",
          context: { order: "4471", amount: "SEK 400" },
          context_order: ["order", "amount"],
          decided_at: "2026-08-24T09:00:00Z",
        },
      ],
    });
    render(<MemoryRouter><Approvals /></MemoryRouter>);

    expect(await screen.findByText(/order: 4471/)).toBeInTheDocument();
    expect(screen.queryByText(/approvals\.noPrompt/)).not.toBeInTheDocument();
  });

  it("shows a cancelled request as its own outcome, with the reason", async () => {
    listDecidedApprovals.mockResolvedValue({
      approvals: [
        {
          run_id: DECIDED_RUN,
          node_id: "approve_1",
          graph_id: FLOW_ID,
          prompt: "Refund order 4471?",
          decision: "cancelled",
          reason: "customer withdrew the claim",
          decided_at: "2026-08-24T09:00:00Z",
        },
      ],
    });
    render(<MemoryRouter><Approvals /></MemoryRouter>);

    await screen.findByText("approvals.historyTitle");
    expect(
      screen.getByRole("img", { name: "approvals.historyCancelled" }),
    ).toBeInTheDocument();
    expect(screen.getByText("customer withdrew the claim")).toBeInTheDocument();
    // Nobody approved it, so the row must not name an approver — nor say one
    // wasn't recorded, as though one should have been.
    expect(screen.queryByText("approvals.historyBy")).not.toBeInTheDocument();
    expect(
      screen.queryByText("approvals.historyByUnknown"),
    ).not.toBeInTheDocument();
  });

  it("renders no history section when nothing has been decided", async () => {
    listDecidedApprovals.mockResolvedValue({ approvals: [] });
    render(<MemoryRouter><Approvals /></MemoryRouter>);

    await waitFor(() => expect(listDecidedApprovals).toHaveBeenCalled());
    expect(screen.queryByText("approvals.historyTitle")).not.toBeInTheDocument();
  });

  it("reloads the history when a decision is made, so the row moves in one beat", async () => {
    listPendingApprovals.mockResolvedValue({
      approvals: [
        {
          run_id: RUN_ID,
          node_id: "approve_1",
          graph_id: FLOW_ID,
          prompt: "Refund 240 SEK?",
          since: "2026-08-24T09:00:00Z",
        },
      ],
    });
    render(<MemoryRouter><Approvals /></MemoryRouter>);

    const approve = await screen.findByRole("button", { name: /common.approve/ });
    listDecidedApprovals.mockClear();
    await userEvent.click(approve);

    await waitFor(() => expect(approveNode).toHaveBeenCalled());
    // Not on a timer: a decided approval never changes again, so the decision
    // itself is what has to pull the history forward.
    await waitFor(() => expect(listDecidedApprovals).toHaveBeenCalled());
  });
});
