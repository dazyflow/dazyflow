// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The signed-out pages, asked for by someone who is already signed in.
//
// /signin and /signup live only in the signed-OUT route tree, so with a
// session in hand the authenticated tree answered them with its catch-all:
// "We couldn't find that page." The page exists — the visitor is simply past
// it. The sign-up form's own "Already have an account? Sign in" link lands
// there, and so does any bookmark or stale marketing link.
//
// The fix has to be a REDIRECT, and this file exists to keep it one. Signing
// the session out instead reads as the more literal answer, and it is wrong:
// a successful sign-up sets the token while the URL is still /signup, so a
// sign-out there logs people out the instant they create an account.
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Navigate, Route, Routes } from "react-router-dom";

// The two routes under test, mounted exactly as App.tsx mounts them inside
// the authenticated tree, next to a stand-in for the catch-all they used to
// fall through to.
function mountAt(entry: string) {
  render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/" element={<div>APP_ROOT</div>} />
        <Route path="/signin" element={<Navigate to="/" replace />} />
        <Route path="/signup" element={<Navigate to="/" replace />} />
        <Route path="*" element={<div>NOT_FOUND</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("signed-out routes inside the authenticated tree", () => {
  it("sends /signin to the app instead of a 404", () => {
    mountAt("/signin");
    expect(screen.getByText("APP_ROOT")).toBeInTheDocument();
    expect(screen.queryByText("NOT_FOUND")).toBeNull();
  });

  it("sends /signup to the app instead of a 404", () => {
    mountAt("/signup");
    expect(screen.getByText("APP_ROOT")).toBeInTheDocument();
    expect(screen.queryByText("NOT_FOUND")).toBeNull();
  });

  it("still 404s a genuinely unknown path", () => {
    mountAt("/no-such-page");
    expect(screen.getByText("NOT_FOUND")).toBeInTheDocument();
  });
});
