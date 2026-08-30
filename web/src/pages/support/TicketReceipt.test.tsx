// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// "Read by customer" under the agent's newest reply — the answer to "did they
// see it?", which otherwise costs a phone call or a second ticket.
//
// Three things make this easy to get subtly wrong, and each is worth a test:
// the receipt is per THREAD rather than per message, absence of a receipt is
// not evidence of not reading, and the mirror of this indicator is deliberately
// NOT offered to the customer.

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && o.time ? `${k}:${String(o.time)}` : k;
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

const getMyTicket = vi.fn();
const getSupportTicket = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    getMyTicket: (...a: unknown[]) => getMyTicket(...a),
    getSupportTicket: (...a: unknown[]) => getSupportTicket(...a),
    getMyTicketBundle: () => Promise.resolve({}),
    getSupportTicketBundle: () => Promise.resolve({}),
    markMyTicketRead: () => Promise.resolve({}),
    markSupportTicketRead: () => Promise.resolve({}),
  },
}));

import { TicketThread } from "./SupportTickets";

const T0 = "2026-07-01T10:00:00Z";
const msg = (id: string, kind: string, at: string, body = id) => ({
  id,
  ticket_id: "tk-1",
  author: kind === "support" ? "agent-a@vendor.com" : "customer@acme.com",
  author_kind: kind,
  body,
  created_at: at,
});

const view = (messages: unknown[], userReadAt?: string) => ({
  ticket: {
    id: "tk-1",
    tenant: "acme",
    workspace: "main",
    created_by: "customer@acme.com",
    subject: "Invoice flow keeps failing",
    status: "awaiting_user",
    created_at: T0,
    updated_at: T0,
    ...(userReadAt ? { user_read_at: userReadAt } : {}),
  },
  messages,
});

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

beforeEach(() => vi.clearAllMocks());

describe("read receipt on the agent's reply", () => {
  it("says the customer read it, and when", async () => {
    getSupportTicket.mockResolvedValue(
      view(
        [msg("m1", "user", "2026-07-01T10:00:00Z"), msg("m2", "support", "2026-07-01T11:00:00Z")],
        "2026-07-01T12:00:00Z",
      ),
    );
    renderThread("agent");
    expect(await screen.findByText(/support\.readByCustomer/)).toBeInTheDocument();
    expect(screen.queryByText("support.notReadYet")).toBeNull();
  });

  it("says not read when the customer last looked BEFORE the reply", async () => {
    getSupportTicket.mockResolvedValue(
      view(
        [msg("m1", "user", "2026-07-01T10:00:00Z"), msg("m2", "support", "2026-07-01T11:00:00Z")],
        "2026-07-01T10:30:00Z",
      ),
    );
    renderThread("agent");
    expect(await screen.findByText("support.notReadYet")).toBeInTheDocument();
  });

  it("shows nothing at all when no receipt was ever recorded", async () => {
    // A ticket predating read tracking, or filed through the API. "Not read
    // yet" there is a confident guess; no badge means no information.
    getSupportTicket.mockResolvedValue(
      view([msg("m1", "user", T0), msg("m2", "support", "2026-07-01T11:00:00Z")]),
    );
    renderThread("agent");
    await screen.findByText("m2");
    expect(screen.queryByText("support.notReadYet")).toBeNull();
    expect(screen.queryByText(/support\.readByCustomer/)).toBeNull();
  });

  it("marks only the NEWEST support message, not every one", async () => {
    // The receipt is per thread. Every older reply has been read too, but
    // tagging them all turns the signal into wallpaper.
    getSupportTicket.mockResolvedValue(
      view(
        [
          msg("m1", "support", "2026-07-01T09:00:00Z"),
          msg("m2", "user", "2026-07-01T10:00:00Z"),
          msg("m3", "support", "2026-07-01T11:00:00Z"),
        ],
        "2026-07-01T12:00:00Z",
      ),
    );
    renderThread("agent");
    expect(await screen.findAllByText(/support\.readByCustomer/)).toHaveLength(1);
  });

  it("puts no receipt on the customer's own message", async () => {
    // "Read by customer" under something the customer wrote is nonsense.
    getSupportTicket.mockResolvedValue(
      view([msg("m1", "user", "2026-07-01T11:00:00Z")], "2026-07-01T12:00:00Z"),
    );
    renderThread("agent");
    await screen.findByText("m1");
    expect(screen.queryByText(/support\.readByCustomer/)).toBeNull();
  });

  it("is never shown on the customer's own view", async () => {
    // The mirror of this — "support opened your ticket and said nothing" — is
    // a stopwatch on the desk, not an answer to a question. The server strips
    // support_read_at; the UI must not offer the indicator either.
    getSupportTicket.mockResolvedValue(null);
    getMyTicket.mockResolvedValue(
      view(
        [msg("m1", "user", "2026-07-01T10:00:00Z"), msg("m2", "support", "2026-07-01T11:00:00Z")],
        "2026-07-01T12:00:00Z",
      ),
    );
    renderThread("user");
    await screen.findByText("m2");
    expect(screen.queryByText(/support\.readByCustomer/)).toBeNull();
    expect(screen.queryByText("support.notReadYet")).toBeNull();
  });
});
