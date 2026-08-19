// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  return { useTranslation: () => ({ t }) };
});
vi.mock("../auth", () => ({
  useAuth: () => ({
    token: "tok-123",
    me: { subject: "user@acme.com" },
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: () => true,
  }),
}));

const listMyTickets = vi.fn();
const listGraphs = vi.fn();
const listRuns = vi.fn();
const createTicket = vi.fn();
vi.mock("../api", () => ({
  APIError: class extends Error {},
  api: {
    listMyTickets: (...a: unknown[]) => listMyTickets(...a),
    listGraphs: (...a: unknown[]) => listGraphs(...a),
    listRuns: (...a: unknown[]) => listRuns(...a),
    createTicket: (...a: unknown[]) => createTicket(...a),
  },
}));

import { SupportTickets } from "./SupportTickets";

const NEW_TICKET = {
  id: "tk-new",
  tenant: "acme",
  workspace: "main",
  created_by: "user@acme.com",
  subject: "Invoice flow keeps failing",
  status: "awaiting_support" as const,
  created_at: "2026-07-01T10:00:00Z",
  updated_at: "2026-07-01T10:00:00Z",
};

beforeEach(() => {
  vi.clearAllMocks();
  listMyTickets.mockResolvedValue({ tickets: [] });
  listGraphs.mockResolvedValue({
    graphs: [
      { id: "daily-invoice", name: "Daily invoice" },
      { id: "webhook-relay", name: "Webhook relay" },
    ],
  });
  listRuns.mockResolvedValue({ runs: [] });
  createTicket.mockResolvedValue(NEW_TICKET);
});

async function openModal() {
  render(
    <MemoryRouter>
      <SupportTickets />
    </MemoryRouter>,
  );
  await screen.findByText("support.empty");
  await userEvent.click(screen.getByRole("button", { name: "support.new" }));
  return screen.findByLabelText("support.flowLabel");
}

describe("New ticket modal", () => {
  it("files without flow context when no flow is picked", async () => {
    await openModal();
    await userEvent.type(screen.getByPlaceholderText("support.subjectPlaceholder"), "Help me");
    await userEvent.click(screen.getByRole("button", { name: "support.send" }));

    await waitFor(() => expect(createTicket).toHaveBeenCalled());
    expect(createTicket.mock.calls[0][1]).toMatchObject({ subject: "Help me" });
    expect(createTicket.mock.calls[0][1].flow_id).toBeUndefined();
    expect(createTicket.mock.calls[0][1].run_id).toBeUndefined();
  });

  // The regression this whole change exists for: picking a flow must reach the
  // server as flow_id, because that is what triggers the redacted bundle.
  it("sends flow_id for the picked flow", async () => {
    const select = await openModal();
    await userEvent.type(screen.getByPlaceholderText("support.subjectPlaceholder"), "Help me");
    await userEvent.selectOptions(select, "webhook-relay");
    await userEvent.click(screen.getByRole("button", { name: "support.send" }));

    await waitFor(() => expect(createTicket).toHaveBeenCalled());
    expect(createTicket.mock.calls[0][1]).toMatchObject({
      subject: "Help me",
      flow_id: "webhook-relay",
    });
  });

  // A failed run makes the bundle far more useful, so it rides along.
  it("attaches the most recent failed run of the picked flow", async () => {
    listRuns.mockResolvedValue({
      runs: [{ id: "run-9", graph_id: "daily-invoice", status: "failed", enqueued_at: "2026-07-01T09:00:00Z" }],
    });
    const select = await openModal();
    await userEvent.type(screen.getByPlaceholderText("support.subjectPlaceholder"), "Help me");
    await userEvent.selectOptions(select, "daily-invoice");
    await waitFor(() => expect(listRuns).toHaveBeenCalled());
    expect(listRuns.mock.calls[0][4]).toMatchObject({ status: "failed", limit: 1 });

    await userEvent.click(screen.getByRole("button", { name: "support.send" }));
    await waitFor(() => expect(createTicket).toHaveBeenCalled());
    expect(createTicket.mock.calls[0][1]).toMatchObject({
      flow_id: "daily-invoice",
      run_id: "run-9",
    });
  });

  // Filing must never be blocked by the optional run lookup.
  it("still files when the run lookup fails", async () => {
    listRuns.mockRejectedValue(new Error("boom"));
    const select = await openModal();
    await userEvent.type(screen.getByPlaceholderText("support.subjectPlaceholder"), "Help me");
    await userEvent.selectOptions(select, "daily-invoice");
    await userEvent.click(screen.getByRole("button", { name: "support.send" }));

    await waitFor(() => expect(createTicket).toHaveBeenCalled());
    expect(createTicket.mock.calls[0][1]).toMatchObject({ flow_id: "daily-invoice" });
    expect(createTicket.mock.calls[0][1].run_id).toBeUndefined();
  });

  // No flows means the picker is noise, so it stays hidden.
  it("hides the picker when the workspace has no flows", async () => {
    listGraphs.mockResolvedValue({ graphs: [] });
    render(
      <MemoryRouter>
        <SupportTickets />
      </MemoryRouter>,
    );
    await screen.findByText("support.empty");
    await userEvent.click(screen.getByRole("button", { name: "support.new" }));
    await screen.findByPlaceholderText("support.subjectPlaceholder");
    expect(screen.queryByLabelText("support.flowLabel")).toBeNull();
  });
});
