// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

// Capture the process-wide 401 handler AuthProvider registers so the test can
// fire it the way api.request() does on an authenticated 401.
let onUnauthorized: (() => void) | null = null;
// whoami never settles: it stands in for the bootstrap request still in flight
// when a *parallel* authenticated call (preferences, or any page fetch) 401s
// first and tears the session down.
const pendingWhoami = new Promise<never>(() => {});

vi.mock("./api", () => ({
  COOKIE_SESSION: "cookie",
  APIError: class APIError extends Error {
    constructor(public status: number, msg: string) {
      super(msg);
    }
  },
  setUnauthorizedHandler: (h: (() => void) | null) => {
    if (h) onUnauthorized = h;
  },
  api: {
    whoami: () => pendingWhoami,
    whoamiProbe: () => Promise.reject(new Error("no cookie")),
    getPreferences: () => Promise.reject(new Error("401")),
  },
}));
vi.mock("./i18n", () => ({
  default: { t: (k: string) => k, resolvedLanguage: "en", changeLanguage: vi.fn() },
}));
vi.mock("./theme", () => ({ applyTheme: vi.fn() }));

import { AuthProvider, useAuth } from "./auth";

// Mirrors the SignIn submit button's gate: disabled={busy || loading || …}.
function SubmitGate() {
  const { loading } = useAuth();
  return <button disabled={loading}>signIn.submit</button>;
}

describe("AuthProvider session expiry", () => {
  beforeEach(() => {
    onUnauthorized = null;
  });

  it("leaves the sign-in submit button enabled after a 401 tears down the session", async () => {
    // A returning user: the marker makes AuthProvider start in the loading
    // state while it re-validates the cookie.
    localStorage.setItem("dazyflow.session", "1");
    render(
      <MemoryRouter>
        <AuthProvider>
          <SubmitGate />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.getByRole("button")).toBeDisabled();

    // The expired session surfaces as an authenticated 401 while the bootstrap
    // whoami is still outstanding — so its .finally() never runs.
    await act(async () => {
      onUnauthorized?.();
    });

    expect(screen.getByRole("button")).toBeEnabled();
  });
});
