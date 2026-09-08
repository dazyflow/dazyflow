// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

// One stable `t` for the whole run: the real react-i18next hands back a stable
// function, and the queue's refresh callback lists `t` as a dependency — a fresh
// identity per render would refetch on every keystroke.
vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  return { useTranslation: () => ({ t }) };
});
vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok-123",
    me: { subject: "agent-a@vendor.com" },
    hasPerm: () => true,
  }),
}));

const listTicketQueue = vi.fn();
const ticketQueueSummary = vi.fn();
const assignSupportTicket = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listTicketQueue: (...a: unknown[]) => listTicketQueue(...a),
    ticketQueueSummary: (...a: unknown[]) => ticketQueueSummary(...a),
    assignSupportTicket: (...a: unknown[]) => assignSupportTicket(...a),
  },
}));

import { SupportQueue } from "./SupportTickets";

const UNCLAIMED = {
  id: "tk-1",
  tenant: "acme",
  workspace: "main",
  created_by: "user@acme.com",
  subject: "Invoice flow keeps failing",
  status: "awaiting_support" as const,
  created_at: "2026-07-01T10:00:00Z",
  updated_at: "2026-07-01T10:00:00Z",
};
const MINE = {
  ...UNCLAIMED,
  id: "tk-2",
  subject: "Webhook never fires",
  assigned_to: "agent-a@vendor.com",
};

const SUMMARY = {
  summary: {
    by_status: { awaiting_support: 2, resolved: 7 },
    total: 9,
    open: 2,
    unassigned: 1,
    by_assignee: { "agent-a@vendor.com": 1 },
  },
  mine: 1,
};

function tileFor(label: string): HTMLElement {
  return screen.getByRole("button", { name: new RegExp(label.replace(/\./g, "\\.")) });
}

function rowFor(subject: string): HTMLElement {
  const el = screen.getByText(subject).closest(".user-card");
  if (!el) throw new Error(`no queue row for ${subject}`);
  return el as HTMLElement;
}

function renderQueue() {
  return render(
    <MemoryRouter>
      <SupportQueue />
    </MemoryRouter>,
  );
}

describe("SupportQueue dashboard", () => {
  beforeEach(() => {
    listTicketQueue.mockReset().mockResolvedValue({ tickets: [UNCLAIMED, MINE] });
    ticketQueueSummary.mockReset().mockResolvedValue(SUMMARY);
    assignSupportTicket.mockReset().mockResolvedValue({});
  });

  it("shows the server-side counts and both tickets, with Claim only on the unclaimed one", async () => {
    renderQueue();
    await waitFor(() => expect(screen.getByText("Invoice flow keeps failing")).toBeInTheDocument());
    expect(screen.getByText("Webhook never fires")).toBeInTheDocument();
    expect(rowFor("Invoice flow keeps failing").textContent).toContain("support.unassigned");
    expect(rowFor("Webhook never fires").textContent).toContain("support.assignedToYou");
    expect(tileFor("support.stats.unassigned").textContent).toContain("1");
    expect(tileFor("support.stats.mine").textContent).toContain("1");
    expect(tileFor("support.stats.waiting").textContent).toContain("2");
    expect(tileFor("support.stats.open").textContent).toContain("2");
    expect(screen.getAllByRole("button", { name: /support\.claim/ })).toHaveLength(1);
    expect(listTicketQueue).toHaveBeenCalledWith("tok-123", {
      status: undefined,
      assignee: undefined,
      unassigned: false,
    });
  });

  it("re-queries with the matching filter when a tile is selected", async () => {
    renderQueue();
    await waitFor(() => expect(ticketQueueSummary).toHaveBeenCalled());

    await userEvent.click(screen.getByRole("button", { name: /support\.stats\.unassigned/ }));
    await waitFor(() =>
      expect(listTicketQueue).toHaveBeenLastCalledWith("tok-123", {
        status: undefined,
        assignee: undefined,
        unassigned: true,
      }),
    );

    await userEvent.click(screen.getByRole("button", { name: /support\.stats\.mine/ }));
    await waitFor(() =>
      expect(listTicketQueue).toHaveBeenLastCalledWith("tok-123", {
        status: undefined,
        assignee: "me",
        unassigned: false,
      }),
    );

    await userEvent.click(screen.getByRole("button", { name: /support\.stats\.waiting/ }));
    await waitFor(() =>
      expect(listTicketQueue).toHaveBeenLastCalledWith("tok-123", {
        status: "awaiting_support",
        assignee: undefined,
        unassigned: false,
      }),
    );
  });

  it("claims a ticket from the list and refreshes", async () => {
    renderQueue();
    await waitFor(() => expect(screen.getByText("Invoice flow keeps failing")).toBeInTheDocument());
    const before = listTicketQueue.mock.calls.length;

    await userEvent.click(screen.getByRole("button", { name: /support\.claim/ }));
    expect(assignSupportTicket).toHaveBeenCalledWith("tok-123", "tk-1", "me");
    await waitFor(() => expect(listTicketQueue.mock.calls.length).toBeGreaterThan(before));
  });

  it("filters the loaded page by free text", async () => {
    renderQueue();
    await waitFor(() => expect(screen.getByText("Webhook never fires")).toBeInTheDocument());

    await userEvent.type(screen.getByRole("searchbox"), "webhook");
    expect(screen.queryByText("Invoice flow keeps failing")).not.toBeInTheDocument();
    expect(screen.getByText("Webhook never fires")).toBeInTheDocument();
    expect(listTicketQueue).toHaveBeenCalledTimes(1);
  });
});
