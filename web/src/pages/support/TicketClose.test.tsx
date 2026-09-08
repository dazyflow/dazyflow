// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later


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
    renderThread();
    await userEvent.click((await screen.findAllByRole("button", { name: /support\.close/ }))[0]);
    expect(await screen.findByText("support.confirmCloseBody")).toBeInTheDocument();
  });

  it("closes when confirmed", async () => {
    renderThread();
    await userEvent.click((await screen.findAllByRole("button", { name: /support\.close/ }))[0]);
    await screen.findByText("support.confirmCloseTitle");

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
