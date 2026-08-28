// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { WhoAmI } from "./types";

// The bootstrap's org reconciliation. pickActive prefers the org this browser
// last used (localStorage) over the org the session is bound to — that is the
// "remember my last org" behaviour and is correct. What must NOT happen is the
// client settling on an org the SESSION isn't scoped to: every scoped call then
// carries ?tenant=<that org> against a principal bound elsewhere and comes back
// 403 forbidden_scope, which the UI can only render as "you don't have
// permission" — a dead end no role grant fixes.

const HOME = "usr_home";
const OTHER = "dev";

function whoAmI(tenant: string): WhoAmI {
  return {
    subject: "member@example.com",
    tenant,
    workspace: "main",
    roles: [],
    permissions: [],
    memberships: [
      { tenant: HOME, workspace: "main", roles: [], home: true },
      { tenant: OTHER, workspace: "main", roles: [], home: false },
    ],
  } as WhoAmI;
}

const switchOrg = vi.fn<(token: string | null, tenant: string) => Promise<unknown>>();
// Each whoami reflects the scope the session currently has: the first answers
// with the home binding, and any call after a successful switch answers with
// the org we switched into.
const whoami = vi.fn(async () => whoAmI(switchedTo ?? HOME));
let switchedTo: string | null = null;

vi.mock("./api", () => ({
  COOKIE_SESSION: "cookie",
  APIError: class APIError extends Error {
    constructor(public status: number, msg: string) {
      super(msg);
    }
  },
  setUnauthorizedHandler: () => {},
  api: {
    whoami: (t: string | null) => whoami(t),
    whoamiProbe: () => Promise.reject(new Error("no cookie")),
    switchOrg: (t: string | null, tenant: string) => switchOrg(t, tenant),
    getPreferences: () => Promise.reject(new Error("no prefs")),
    listTenants: () => Promise.resolve({ tenants: [] }),
  },
}));
vi.mock("./i18n", () => ({
  default: { t: (k: string) => k, resolvedLanguage: "en", changeLanguage: vi.fn() },
}));
vi.mock("./theme", () => ({ applyTheme: vi.fn() }));

import { AuthProvider, useAuth } from "./auth";

function ActiveTenant() {
  const { activeTenant } = useAuth();
  return <span data-testid="active">{activeTenant || "-"}</span>;
}

function mount() {
  localStorage.setItem("dazyflow.session", "1");
  render(
    <MemoryRouter>
      <AuthProvider>
        <ActiveTenant />
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("AuthProvider bootstrap org reconciliation", () => {
  beforeEach(() => {
    localStorage.clear();
    switchedTo = null;
    switchOrg.mockReset();
    whoami.mockClear();
  });

  it("re-scopes the session when the cached org differs from the bound one", async () => {
    // A fresh sign-in binds the session to the home org, but this browser last
    // used the invited org.
    localStorage.setItem("dazyflow.activeTenant", OTHER);
    switchOrg.mockImplementation(async (_t, tenant) => {
      switchedTo = tenant;
    });

    mount();

    await waitFor(() => expect(switchOrg).toHaveBeenCalledWith("cookie", OTHER));
    await waitFor(() => expect(screen.getByTestId("active")).toHaveTextContent(OTHER));
    // The re-scoped identity is adopted, so the rest of the app (and every
    // scoped request) agrees with the server about which org it is in.
    expect(whoami).toHaveBeenCalledTimes(2);
  });

  it("falls back to the bound org when the server refuses to re-scope", async () => {
    // The membership was revoked since this browser last used that org.
    localStorage.setItem("dazyflow.activeTenant", OTHER);
    switchOrg.mockRejectedValue(new Error("forbidden"));

    mount();

    await waitFor(() => expect(screen.getByTestId("active")).toHaveTextContent(HOME));
    // The cache is repaired, so the next cold boot doesn't retry the dead org.
    expect(localStorage.getItem("dazyflow.activeTenant")).toBe(HOME);
  });

  it("does not round-trip when the cached org already matches the binding", async () => {
    localStorage.setItem("dazyflow.activeTenant", HOME);

    mount();

    await waitFor(() => expect(screen.getByTestId("active")).toHaveTextContent(HOME));
    expect(switchOrg).not.toHaveBeenCalled();
  });
});
