// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
vi.mock("../auth", () => ({
  useAuth: () => ({
    token: "tok-123",
    me: { subject: "agent-a@vendor.com" },
    hasPerm: () => true,
  }),
}));

const listTicketQueue = vi.fn();
const ticketQueueSummary = vi.fn();
const assignSupportTicket = vi.fn();
vi.mock("../api", () => ({
  APIError: class extends Error {},
  api: {
    listTicketQueue: (...a: unknown[]) => listTicketQueue(...a),
    ticketQueueSummary: (...a: unknown[]) => ticketQueueSummary(...a),
    assignSupportTicket: (...a: unknown[]) => assignSupportTicket(...a),
  },
}));

import { SupportQueue } from "./SupportTickets";

// Two tickets: one nobody has claimed, one this agent already owns.
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

// tileFor finds a dashboard stat tile by its label.
function tileFor(label: string): HTMLElement {
  return screen.getByRole("button", { name: new RegExp(label.replace(/\./g, "\\.")) });
}

// rowFor finds the queue row carrying a given subject.
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
    // The tiles read the summary, not the loaded page: 1 unassigned / 1 mine /
    // 2 awaiting support / 2 open.
    await waitFor(() => expect(screen.getByText("Invoice flow keeps failing")).toBeInTheDocument());
    expect(screen.getByText("Webhook never fires")).toBeInTheDocument();
    // Ownership is shown per row (the meta line mixes several fragments, so
    // assert on the row's text rather than a lone node).
    expect(rowFor("Invoice flow keeps failing").textContent).toContain("support.unassigned");
    expect(rowFor("Webhook never fires").textContent).toContain("support.assignedToYou");
    // The tiles report the server's counts over the whole queue (1 unassigned,
    // 1 mine, 2 awaiting support, 2 open of 9 filed) — not the page's 2 rows.
    expect(tileFor("support.stats.unassigned").textContent).toContain("1");
    expect(tileFor("support.stats.mine").textContent).toContain("1");
    expect(tileFor("support.stats.waiting").textContent).toContain("2");
    expect(tileFor("support.stats.open").textContent).toContain("2");
    // One Claim button — the ticket this agent already owns doesn't offer one.
    expect(screen.getAllByRole("button", { name: /support\.claim/ })).toHaveLength(1);
    // The first load asks for everything (no ownership/status narrowing).
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

    // The status tile narrows on status instead of ownership.
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
    // Claiming re-reads the queue so the row moves out of the unassigned view.
    await waitFor(() => expect(listTicketQueue.mock.calls.length).toBeGreaterThan(before));
  });

  it("filters the loaded page by free text", async () => {
    renderQueue();
    await waitFor(() => expect(screen.getByText("Webhook never fires")).toBeInTheDocument());

    await userEvent.type(screen.getByRole("searchbox"), "webhook");
    expect(screen.queryByText("Invoice flow keeps failing")).not.toBeInTheDocument();
    expect(screen.getByText("Webhook never fires")).toBeInTheDocument();
    // Searching is client-side over what's already loaded — no extra request.
    expect(listTicketQueue).toHaveBeenCalledTimes(1);
  });
});
