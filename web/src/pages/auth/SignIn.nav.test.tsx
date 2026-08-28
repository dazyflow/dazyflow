// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guards where a completed sign-in LANDS. The authenticated route tree has no
// /signin route, so a sign-in that navigates nowhere leaves the router sitting
// on /signin and the authenticated catch-all renders "page not found" at the
// exact moment the user succeeded. Every success path must navigate.
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const signInWithPassword = vi.fn();
vi.mock("../../auth", () => ({
  useAuth: () => ({
    signInWithPassword,
    verifyTOTP: vi.fn(),
    error: null,
    loading: false,
    clearError: () => {},
  }),
}));

vi.mock("../../api", () => ({
  api: {
    getPublicAuthConfig: () => Promise.resolve({ signup_enabled: true }),
    getPublicSSOStatus: () => Promise.resolve({ google_enabled: false }),
    resolveSubdomain: () => Promise.reject(new Error("no subdomain")),
  },
}));

import { SignIn } from "./SignIn";

function mountAt(entry: string) {
  render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/signin" element={<SignIn />} />
        <Route path="/" element={<div>ROOT</div>} />
        <Route path="/invite/:token" element={<div>INVITE</div>} />
        <Route path="/runs/abc" element={<div>RETURN_TO</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

async function signIn() {
  const user = userEvent.setup();
  // Query by id/type rather than label text: the t() mock renders raw keys, and
  // the password reveal toggle's aria-label also contains "password".
  await user.type(document.getElementById("email") as HTMLInputElement, "a@b.c");
  await user.type(document.getElementById("password") as HTMLInputElement, "pw");
  await user.click(screen.getByRole("button", { name: "common.signIn" }));
}

describe("SignIn post-sign-in navigation", () => {
  beforeEach(() => {
    signInWithPassword.mockReset();
    signInWithPassword.mockResolvedValue({});
  });

  it("leaves /signin for the root once a plain sign-in succeeds", async () => {
    mountAt("/signin");
    await signIn();
    await waitFor(() => expect(screen.getByText("ROOT")).toBeInTheDocument());
  });

  it("resumes the invite flow when the deep link carried one", async () => {
    mountAt("/signin?invite=inv_123");
    await signIn();
    await waitFor(() => expect(screen.getByText("INVITE")).toBeInTheDocument());
  });

  it("honours a rooted return_to", async () => {
    mountAt("/signin?return_to=%2Fruns%2Fabc");
    await signIn();
    await waitFor(() => expect(screen.getByText("RETURN_TO")).toBeInTheDocument());
  });

  it("ignores an off-origin return_to and lands on the root", async () => {
    mountAt("/signin?return_to=%2F%2Fevil.com");
    await signIn();
    await waitFor(() => expect(screen.getByText("ROOT")).toBeInTheDocument());
  });
});
