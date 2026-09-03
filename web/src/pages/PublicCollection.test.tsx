// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The public collection page — the login-free table behind a share link.
//
// The reader here has no account and no other route into the app, so the two
// things this page must never do are render nothing for a dead link and shove
// unreadable stored values at somebody. Everything else is a table.

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Routes, Route } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o === "object" ? `${k}:${Object.values(o).join(",")}` : k;
  return { useTranslation: () => ({ t, i18n: { language: "en" } }) };
});

const getPublicCollection = vi.fn();
// The factory is hoisted above the module body, so it may not close over
// anything declared here — hence the shape check rather than an instanceof
// against a class defined in this file.
vi.mock("../api", () => ({
  APIError: class extends Error {},
  isErrorCode: (e: unknown, code: string) =>
    !!e && typeof e === "object" && (e as { code?: string }).code === code,
  api: {
    getPublicCollection: (...a: unknown[]) => getPublicCollection(...a),
  },
}));

// apiError builds what the real APIError looks like to isErrorCode: an Error
// carrying the server's error code.
function apiError(code: string): Error & { code: string } {
  return Object.assign(new Error(code), { code });
}

import { PublicCollection } from "./PublicCollection";

const TOKEN = "abc123";

function renderPage() {
  return render(
    <MemoryRouter initialEntries={[`/board/${TOKEN}`]}>
      <Routes>
        <Route path="/board/:token" element={<PublicCollection />} />
      </Routes>
    </MemoryRouter>,
  );
}

const leads = {
  label: "Acme AB",
  collection: "leads",
  generated_at: "2026-09-03T10:00:00Z",
  columns: ["email", "saved_at"],
  rows: [
    { email: "a@example.com", saved_at: "2026-08-31T07:21:54Z" },
    { email: "b@example.com", saved_at: "2026-08-31T08:00:00Z" },
  ],
  total: 2,
  offset: 0,
};

beforeEach(() => {
  getPublicCollection.mockReset();
});

describe("PublicCollection", () => {
  it("renders the collection as a table under the org's name", async () => {
    getPublicCollection.mockResolvedValue(leads);
    renderPage();

    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "leads" })).toBeInTheDocument(),
    );
    expect(screen.getByText("Acme AB")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "email" })).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "a@example.com" })).toBeInTheDocument();
  });

  it("passes the token and the window to the API", async () => {
    getPublicCollection.mockResolvedValue(leads);
    renderPage();
    await waitFor(() =>
      expect(getPublicCollection).toHaveBeenCalledWith(TOKEN, {
        limit: 1000,
        offset: 0,
      }),
    );
  });

  // The stored value is a UTC instant; the reader should not be doing UTC
  // arithmetic on a page they were sent.
  it("renders a timestamp cell in local time, not as the stored instant", async () => {
    getPublicCollection.mockResolvedValue(leads);
    const { container } = renderPage();
    await waitFor(() =>
      expect(screen.getByRole("columnheader", { name: "email" })).toBeInTheDocument(),
    );
    const cells = [...container.querySelectorAll("tbody tr td")].map(
      (td) => td.textContent,
    );
    expect(cells).not.toContain("2026-08-31T07:21:54Z");
  });

  // A dead link is the whole screen, so it has to say so — an empty page reads
  // as broken rather than as a link somebody turned off.
  it("explains a revoked link instead of rendering nothing", async () => {
    getPublicCollection.mockRejectedValue(apiError("share_not_found"));
    renderPage();
    await waitFor(() =>
      expect(
        screen.getByText("publicCollection.notFoundTitle"),
      ).toBeInTheDocument(),
    );
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("keeps the last good table when a refresh fails", async () => {
    getPublicCollection.mockResolvedValue(leads);
    renderPage();
    await waitFor(() =>
      expect(screen.getByRole("cell", { name: "a@example.com" })).toBeInTheDocument(),
    );

    getPublicCollection.mockRejectedValue(new Error("network"));
    await userEvent.click(
      screen.getByRole("button", { name: /publicCollection\.refresh/ }),
    );
    await waitFor(() =>
      expect(screen.getByText("publicCollection.stale")).toBeInTheDocument(),
    );
    // Still readable.
    expect(screen.getByRole("cell", { name: "a@example.com" })).toBeInTheDocument();
  });

  it("filters the loaded rows as you search", async () => {
    getPublicCollection.mockResolvedValue(leads);
    renderPage();
    await waitFor(() =>
      expect(screen.getByRole("cell", { name: "a@example.com" })).toBeInTheDocument(),
    );

    await userEvent.type(
      screen.getByRole("searchbox", { name: /searchPlaceholder/ }),
      "b@",
    );
    await waitFor(() =>
      expect(screen.queryByRole("cell", { name: "a@example.com" })).toBeNull(),
    );
    expect(screen.getByRole("cell", { name: "b@example.com" })).toBeInTheDocument();
  });

  it("hides the pager for a collection that fits on one page", async () => {
    getPublicCollection.mockResolvedValue(leads);
    renderPage();
    await waitFor(() =>
      expect(screen.getByRole("cell", { name: "a@example.com" })).toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("button", { name: /common\.nextPage/ }),
    ).toBeNull();
    expect(
      screen.getByText(/publicCollection\.rowRange:1,2,2/),
    ).toBeInTheDocument();
  });

  it("pages a collection larger than one window", async () => {
    getPublicCollection.mockResolvedValue({ ...leads, total: 2500 });
    renderPage();
    await waitFor(() =>
      expect(screen.getByRole("cell", { name: "a@example.com" })).toBeInTheDocument(),
    );

    getPublicCollection.mockResolvedValue({
      ...leads,
      total: 2500,
      offset: 1000,
      rows: [{ email: "z@example.com", saved_at: "" }],
    });
    await userEvent.click(
      screen.getByRole("button", { name: /common\.nextPage/ }),
    );
    await waitFor(() =>
      expect(getPublicCollection).toHaveBeenLastCalledWith(TOKEN, {
        limit: 1000,
        offset: 1000,
      }),
    );
    await waitFor(() =>
      expect(screen.getByRole("cell", { name: "z@example.com" })).toBeInTheDocument(),
    );
  });

  it("says so when the collection is empty", async () => {
    getPublicCollection.mockResolvedValue({ ...leads, rows: [], total: 0 });
    renderPage();
    await waitFor(() =>
      expect(screen.getByText("publicCollection.empty")).toBeInTheDocument(),
    );
  });

  // Nothing on this page may change anything: the reader is not signed in and
  // the link is read-only by design.
  it("offers no destructive action", async () => {
    getPublicCollection.mockResolvedValue(leads);
    renderPage();
    await waitFor(() =>
      expect(screen.getByRole("cell", { name: "a@example.com" })).toBeInTheDocument(),
    );
    for (const btn of screen.getAllByRole("button")) {
      expect(btn.textContent ?? "").not.toMatch(/clear|delete|remove/i);
    }
  });
});
