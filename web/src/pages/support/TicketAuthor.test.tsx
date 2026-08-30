// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Who a chat bubble says it is from.
//
// "support.fromYou" was in the code and in both locales from the start, but
// behind `m.author || t("support.fromYou")` — a fallback for a missing author,
// and the server always sends one. So the label never rendered, and every
// message you sent was headed with your own email address read back at you.
//
// The interesting cases are on the agent side, where "mine" (which side of
// the thread the bubble sits on) and "me" (who actually typed it) come apart.

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  return { useTranslation: () => ({ t }) };
});

const me = { subject: "" };
vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok-123",
    me,
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: () => true,
  }),
}));

const getMyTicket = vi.fn();
const getSupportTicket = vi.fn();
const markMyTicketRead = vi.fn();
const markSupportTicketRead = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    getMyTicket: (...a: unknown[]) => getMyTicket(...a),
    getSupportTicket: (...a: unknown[]) => getSupportTicket(...a),
    getMyTicketBundle: () => Promise.resolve({}),
    getSupportTicketBundle: () => Promise.resolve({}),
    markMyTicketRead: (...a: unknown[]) => markMyTicketRead(...a),
    markSupportTicketRead: (...a: unknown[]) => markSupportTicketRead(...a),
  },
}));

import { TicketThread } from "./SupportTickets";

const msg = (over: Record<string, unknown>) => ({
  id: "m-1",
  ticket_id: "tk-1",
  body: "hello",
  created_at: "2026-07-01T10:00:00Z",
  ...over,
});

const view = (messages: unknown[]) => ({
  ticket: {
    id: "tk-1",
    tenant: "acme",
    workspace: "main",
    created_by: "customer@acme.com",
    subject: "Invoice flow keeps failing",
    status: "awaiting_support" as const,
    created_at: "2026-07-01T10:00:00Z",
    updated_at: "2026-07-01T10:00:00Z",
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

beforeEach(() => {
  vi.clearAllMocks();
  me.subject = "customer@acme.com";
  markMyTicketRead.mockResolvedValue({});
  markSupportTicketRead.mockResolvedValue({});
});

describe("who a message is from", () => {
  it("says You instead of reading your own address back at you", async () => {
    getMyTicket.mockResolvedValue(
      view([msg({ author: "customer@acme.com", author_kind: "user" })]),
    );
    renderThread("user");

    expect(await screen.findByText(/support\.fromYou/)).toBeInTheDocument();
    expect(screen.queryByText(/customer@acme\.com/)).toBeNull();
  });

  it("still hides which agent replied from the customer", async () => {
    // Deliberate, and unchanged: the customer sees one "Support", never the
    // individual. Assignment is internal to the support side.
    getMyTicket.mockResolvedValue(
      view([msg({ author: "agent-a@vendor.com", author_kind: "support" })]),
    );
    renderThread("user");

    expect(await screen.findByText(/support\.fromSupport/)).toBeInTheDocument();
    expect(screen.queryByText(/agent-a@vendor\.com/)).toBeNull();
  });

  it("names the customer to the agent, rather than calling them You", async () => {
    me.subject = "agent-a@vendor.com";
    getSupportTicket.mockResolvedValue(
      view([msg({ author: "customer@acme.com", author_kind: "user" })]),
    );
    renderThread("agent");

    expect(await screen.findByText(/customer@acme\.com/)).toBeInTheDocument();
    expect(screen.queryByText(/support\.fromYou/)).toBeNull();
  });

  it("tells an agent their own reply from a colleague's", async () => {
    // Both are "mine" for alignment — every support reply sits on the support
    // side of an agent's screen. Only one of them is actually theirs, and
    // calling the other "You" would misattribute a colleague's words.
    me.subject = "agent-a@vendor.com";
    getSupportTicket.mockResolvedValue(
      view([
        msg({ id: "m-1", author: "agent-a@vendor.com", author_kind: "support", body: "mine" }),
        msg({ id: "m-2", author: "agent-b@vendor.com", author_kind: "support", body: "theirs" }),
      ]),
    );
    renderThread("agent");

    expect(await screen.findByText(/support\.fromYou/)).toBeInTheDocument();
    expect(screen.getByText(/support\.fromSupport/)).toBeInTheDocument();
  });

  it("gives a system note no author label at all", async () => {
    // Narration, not a party in the conversation.
    getMyTicket.mockResolvedValue(
      view([msg({ author_kind: "system", body: "whatever" })]),
    );
    renderThread("user");

    expect(await screen.findByText("whatever")).toBeInTheDocument();
    expect(screen.queryByText(/support\.fromYou/)).toBeNull();
  });
});

// The daemon writes system notes as English prose in `body` plus a
// `system_code`. The prose is right for an API reader and an email digest and
// wrong for a translated UI, which was dropping "The customer closed this
// ticket." into the middle of a Swedish thread.
describe("system notes", () => {
  const note = (over: Record<string, unknown>) =>
    msg({ author_kind: "system", body: "ENGLISH FALLBACK", ...over });

  it("renders the translation, not the daemon's English", async () => {
    getMyTicket.mockResolvedValue(view([note({ system_code: "customer_closed" })]));
    renderThread("user");

    expect(await screen.findByText("support.note.customerClosedYou")).toBeInTheDocument();
    expect(screen.queryByText("ENGLISH FALLBACK")).toBeNull();
  });

  it("narrates your own action in the second person, and to an agent in the third", async () => {
    getMyTicket.mockResolvedValue(view([note({ system_code: "customer_reopened" })]));
    const { unmount } = renderThread("user");
    expect(await screen.findByText("support.note.customerReopenedYou")).toBeInTheDocument();
    unmount();

    me.subject = "agent-a@vendor.com";
    getSupportTicket.mockResolvedValue(view([note({ system_code: "customer_reopened" })]));
    renderThread("agent");
    expect(await screen.findByText("support.note.customerReopened")).toBeInTheDocument();
  });

  it("translates a support-side note on BOTH surfaces", async () => {
    // Regression: the mode check first returned "" for an agent, and "" is not
    // null, so `??` kept it and every note fell through to the English body on
    // the whole support side.
    me.subject = "agent-a@vendor.com";
    getSupportTicket.mockResolvedValue(view([note({ system_code: "marked_resolved" })]));
    renderThread("agent");

    expect(await screen.findByText("support.note.markedResolved")).toBeInTheDocument();
    expect(screen.queryByText("ENGLISH FALLBACK")).toBeNull();
  });

  it("falls back to the English body for a code it does not know", async () => {
    // An older row written before system_code existed, or a daemon newer than
    // this build. English beats a blank bubble or a raw key.
    getMyTicket.mockResolvedValue(view([note({ system_code: "invented_by_a_newer_daemon" })]));
    renderThread("user");

    expect(await screen.findByText("ENGLISH FALLBACK")).toBeInTheDocument();
  });

  it("falls back for a row with no code at all", async () => {
    getMyTicket.mockResolvedValue(view([note({})]));
    renderThread("user");

    expect(await screen.findByText("ENGLISH FALLBACK")).toBeInTheDocument();
  });
});

// Opening the thread has to TELL the server, or the reminder sweep cannot
// distinguish "hasn't answered" from "hasn't looked" and mails people who are
// already up to date — which is how a notification gets filtered.
describe("read receipts", () => {
  it("reports the thread as read when the customer opens it", async () => {
    getMyTicket.mockResolvedValue(view([msg({ author_kind: "user", author: "customer@acme.com" })]));
    renderThread("user");

    await waitFor(() => expect(markMyTicketRead).toHaveBeenCalledWith("tok-123", "tk-1"));
    expect(markSupportTicketRead).not.toHaveBeenCalled();
  });

  it("uses the agent endpoint on the queue side", async () => {
    // The two surfaces stamp different sides. Calling the customer's endpoint
    // as an agent would mark the CUSTOMER up to date and silence their nudge.
    me.subject = "agent-a@vendor.com";
    getSupportTicket.mockResolvedValue(view([msg({ author_kind: "user", author: "customer@acme.com" })]));
    renderThread("agent");

    await waitFor(() => expect(markSupportTicketRead).toHaveBeenCalledWith("tok-123", "tk-1"));
    expect(markMyTicketRead).not.toHaveBeenCalled();
  });

  it("still renders the thread when recording the read fails", async () => {
    // Best-effort: a failed receipt costs one extra reminder, which is not
    // worth an error in front of someone trying to read their ticket.
    markMyTicketRead.mockRejectedValue(new Error("nope"));
    getMyTicket.mockResolvedValue(view([msg({ author_kind: "user", author: "customer@acme.com", body: "still here" })]));
    renderThread("user");

    expect(await screen.findByText("still here")).toBeInTheDocument();
  });
});
