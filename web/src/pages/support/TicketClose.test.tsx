// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Closing a ticket asks first.
//
// "Close ticket" sits in a row with Claim and Release and directly above the
// composer, and it used to fire on the first click — the requester telling
// support to stop, with no step in between. It is undone by replying rather
// than by an Undo, which is a recovery you have to already know about; the
// dialog is where that gets said.

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  return { useTranslation: () => ({ t }) };
});
vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok-123",
    me: { subject: "user@acme.com" },
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: () => true,
  }),
}));

const getMyTicket = vi.fn();
const setMyTicketStatus = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    getMyTicket: (...a: unknown[]) => getMyTicket(...a),
    getSupportTicket: () => Promise.resolve(null),
    setMyTicketStatus: (...a: unknown[]) => setMyTicketStatus(...a),
    getMyTicketBundle: () => Promise.resolve({}),
    // Fired when the thread mounts, so the reminder sweep can tell
    // "hasn't answered" from "hasn't looked". Stubbed here because
    // these tests are about other things and an unmocked call throws.
    markMyTicketRead: () => Promise.resolve({}),
    markSupportTicketRead: () => Promise.resolve({}),
  },
}));

import { TicketThread } from "./SupportTickets";

const VIEW = {
  ticket: {
    id: "tk-1",
    tenant: "acme",
    workspace: "main",
    created_by: "user@acme.com",
    subject: "Invoice flow keeps failing",
    status: "awaiting_support" as const,
    flow_id: "daily-invoice",
    bundle_id: "b-1",
    created_at: "2026-07-01T10:00:00Z",
    updated_at: "2026-07-01T10:00:00Z",
  },
  messages: [],
};

function renderThread() {
  return render(
    <MemoryRouter initialEntries={["/support/tk-1"]}>
      <Routes>
        <Route path="/support/:id" element={<TicketThread mode="user" />} />
      </Routes>
    </MemoryRouter>,
  );
}

// The Close button, told apart from the dialog's confirm button of the same
// name by which one is on screen at the time.
const closeButtons = () => screen.getAllByRole("button", { name: /support\.close/ });

beforeEach(() => {
  vi.clearAllMocks();
  getMyTicket.mockResolvedValue(VIEW);
  setMyTicketStatus.mockResolvedValue(VIEW);
});

describe("closing a ticket", () => {
  it("asks before closing instead of just closing", async () => {
    renderThread();
    await userEvent.click((await screen.findAllByRole("button", { name: /support\.close/ }))[0]);

    expect(await screen.findByText("support.confirmCloseTitle")).toBeInTheDocument();
    // The click that opened the dialog must not also have done the thing.
    expect(setMyTicketStatus).not.toHaveBeenCalled();
  });

  it("says that replying reopens it, so the choice is informed", async () => {
    // The one fact that makes this decision easy, and the one a user has no
    // way to know — there is no Reopen button to infer it from.
    renderThread();
    await userEvent.click((await screen.findAllByRole("button", { name: /support\.close/ }))[0]);
    expect(await screen.findByText("support.confirmCloseBody")).toBeInTheDocument();
  });

  it("closes when confirmed", async () => {
    renderThread();
    await userEvent.click((await screen.findAllByRole("button", { name: /support\.close/ }))[0]);
    await screen.findByText("support.confirmCloseTitle");

    // Two buttons carry this label now; the dialog's is the last mounted.
    const buttons = closeButtons();
    await userEvent.click(buttons[buttons.length - 1]);

    await waitFor(() =>
      expect(setMyTicketStatus).toHaveBeenCalledWith("tok-123", "tk-1", "closed"),
    );
    expect(screen.queryByText("support.confirmCloseTitle")).toBeNull();
  });

  it("does nothing when cancelled", async () => {
    renderThread();
    await userEvent.click((await screen.findAllByRole("button", { name: /support\.close/ }))[0]);
    await screen.findByText("support.confirmCloseTitle");

    await userEvent.click(screen.getByRole("button", { name: "common.cancel" }));

    await waitFor(() => expect(screen.queryByText("support.confirmCloseTitle")).toBeNull());
    expect(setMyTicketStatus).not.toHaveBeenCalled();
    // And the ticket is still closable — cancelling is not a dead end.
    expect(closeButtons().length).toBeGreaterThan(0);
  });

  it("offers no Close button once the ticket is already closed", async () => {
    getMyTicket.mockResolvedValue({
      ...VIEW,
      ticket: { ...VIEW.ticket, status: "closed" as const },
    });
    renderThread();
    await screen.findByText("Invoice flow keeps failing");
    expect(screen.queryByRole("button", { name: /support\.close/ })).toBeNull();
  });
});
