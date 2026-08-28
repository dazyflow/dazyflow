// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { ErrorBoundary } from "./ErrorBoundary";

function Boom(): JSX.Element {
  throw new Error("kaboom from a child");
}

describe("ErrorBoundary", () => {
  // React logs every caught error to console.error by design. Silenced so a
  // passing run stays readable — and asserted on below, because that log is
  // the only record a crash leaves in this build.
  let spy: ReturnType<typeof vi.spyOn>;
  beforeEach(() => {
    spy = vi.spyOn(console, "error").mockImplementation(() => {});
  });
  afterEach(() => spy.mockRestore());

  it("renders children untouched when nothing throws", () => {
    render(
      <ErrorBoundary home="/">
        <p>all is well</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText("all is well")).toBeInTheDocument();
    expect(screen.queryByRole("heading")).not.toBeInTheDocument();
  });

  it("catches a render error and offers a way out instead of a blank page", () => {
    render(
      <ErrorBoundary home="/flows">
        <Boom />
      </ErrorBoundary>,
    );

    // The failure this exists to prevent is an EMPTY document, so assert on
    // what the user can actually see and do.
    expect(
      screen.getByRole("heading", { name: "Something went wrong" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Reload the page" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go back" })).toHaveAttribute(
      "href",
      "/flows",
    );
  });

  it("keeps the error message available for a support report", () => {
    render(
      <ErrorBoundary home="/">
        <Boom />
      </ErrorBoundary>,
    );
    // Collapsed in a <details>, but present in the DOM either way — the point
    // is that it can be copied, not that it is on screen.
    expect(screen.getByText(/kaboom from a child/)).toBeInTheDocument();
    expect(spy).toHaveBeenCalled();
  });

  it("does not translate its own copy", () => {
    // Deliberate: react-i18next reads a module-global instance that a failed
    // bootstrap may never have initialised, so a translated crash screen is
    // one that vanishes exactly when a bootstrap error is what you need to
    // see. This renders with no i18n provider in scope at all — if someone
    // adds useTranslation to the fallback, this test is what fails.
    render(
      <ErrorBoundary home="/">
        <Boom />
      </ErrorBoundary>,
    );
    expect(screen.getByText("Something went wrong")).toBeInTheDocument();
  });
});
