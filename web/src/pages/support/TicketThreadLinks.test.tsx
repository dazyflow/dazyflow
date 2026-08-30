// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  return { useTranslation: () => ({ t }) };
});
vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok-123",
    me: { subject: "agent-a@vendor.com" },
    activeTenant: "vendor",
    activeWorkspace: "main",
    hasPerm: () => true,
  }),
}));

const getSupportTicket = vi.fn();
const getMyTicket = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    getSupportTicket: (...a: unknown[]) => getSupportTicket(...a),
    getMyTicket: (...a: unknown[]) => getMyTicket(...a),
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

function renderThread(mode: "user" | "agent") {
  const path = mode === "agent" ? "/support/queue/tk-1" : "/support/tk-1";
  const route = mode === "agent" ? "/support/queue/:id" : "/support/:id";
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path={route} element={<TicketThread mode={mode} />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  getSupportTicket.mockResolvedValue(VIEW);
  getMyTicket.mockResolvedValue(VIEW);
});

describe("TicketThread flow link", () => {
  // An agent is in a different tenant, so /flows/<id> would resolve to nothing
  // in THEIR workspace. They must be sent to the grant-gated support view.
  it("points an agent at the tenant-scoped support view", async () => {
    renderThread("agent");
    const link = await screen.findByRole("link", { name: "support.viewFlow" });
    expect(link.getAttribute("href")).toBe("/support/flows/acme/main/daily-invoice");
  });

  it("points the owner at their own flow", async () => {
    renderThread("user");
    const link = await screen.findByRole("link", { name: "support.viewFlow" });
    expect(link.getAttribute("href")).toBe("/flows/daily-invoice");
  });

  it("tells the agent when a ticket carries no diagnostic", async () => {
    getSupportTicket.mockResolvedValue({
      ...VIEW,
      ticket: { ...VIEW.ticket, flow_id: "", bundle_id: "" },
    });
    renderThread("agent");
    expect(await screen.findByText("support.noBundle")).toBeTruthy();
  });

  // The customer already knows their own ticket has no diagnostic; that note is
  // internal support chatter, so it stays off their side.
  it("keeps the no-diagnostic note off the customer's view", async () => {
    getMyTicket.mockResolvedValue({
      ...VIEW,
      ticket: { ...VIEW.ticket, flow_id: "", bundle_id: "" },
    });
    renderThread("user");
    await screen.findByText("Invoice flow keeps failing");
    expect(screen.queryByText("support.noBundle")).toBeNull();
  });
});
