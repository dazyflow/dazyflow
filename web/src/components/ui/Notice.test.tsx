// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { Notice } from "./Notice";
import { Loading } from "./Loading";
import { EmptyState } from "./EmptyState";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

describe("Notice", () => {
  // The point of the component: one shell, so a quiet message can't drift back
  // into 42 hand-built muted cards.
  it("renders a card by default", () => {
    const { container } = render(<Notice>Nothing yet</Notice>);
    const el = container.firstElementChild!;
    expect(el.className).toBe("card notice");
    expect(el.textContent).toBe("Nothing yet");
  });

  // Inside a dialog or panel a second card reads as a box inside a box.
  it("drops the card chrome when inline", () => {
    const { container } = render(<Notice inline>Nothing yet</Notice>);
    expect(container.firstElementChild!.className).toBe("notice-line");
  });

  it("appends a caller class without losing the shared one", () => {
    const { container } = render(<Notice className="run-note">x</Notice>);
    expect(container.firstElementChild!.className).toBe("card notice run-note");
  });

  // A muted line is very often just text in a layout; announcing every one of
  // them would be noise, so the role is opt-in rather than baked in.
  it("has no role unless asked for one", () => {
    const { container } = render(<Notice>x</Notice>);
    expect(container.firstElementChild!.getAttribute("role")).toBeNull();
  });
});

describe("Loading", () => {
  it("renders the shared loading string in a card", () => {
    render(<Loading />);
    const el = screen.getByRole("status");
    expect(el.textContent).toBe("common.loading");
    expect(el.className).toBe("card notice");
  });

  // The a11y fix this component exists to carry: all 51 hand-written versions
  // were silent, so a screen-reader user got a page that simply went quiet.
  it("announces itself as a status", () => {
    render(<Loading inline />);
    const el = screen.getByRole("status");
    expect(el.className).toBe("notice-line");
  });
});

describe("EmptyState", () => {
  const Glyph = ((props: Record<string, unknown>) => (
    <svg data-testid="glyph" {...props} />
  )) as never;

  it("renders glyph, heading, body and action", () => {
    render(
      <EmptyState icon={Glyph} title="No keys yet" action={<button>Issue one</button>}>
        Keys let a script talk to the daemon.
      </EmptyState>,
    );
    expect(screen.getByRole("heading", { name: "No keys yet" })).toBeTruthy();
    expect(screen.getByText("Keys let a script talk to the daemon.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Issue one" })).toBeTruthy();
    expect(screen.getByTestId("glyph").getAttribute("aria-hidden")).toBe("true");
  });

  // A no-search-results state has nothing to say above the line explaining
  // itself, and a heading there would only repeat it.
  it("omits the heading when no title is given", () => {
    render(<EmptyState icon={Glyph}>No matches.</EmptyState>);
    expect(screen.queryByRole("heading")).toBeNull();
    expect(screen.getByText("No matches.")).toBeTruthy();
  });

  it("omits the action row when there is no action", () => {
    const { container } = render(<EmptyState icon={Glyph}>Nothing.</EmptyState>);
    expect(container.querySelector(".empty-state-actions")).toBeNull();
  });

  // The old .admin-empty rule tinted `> svg`, which forced a comment warning
  // that the CTA's own Plus icon must not be nested where it would be caught.
  // Targeting a class instead makes that impossible by construction, so an
  // icon inside the action keeps its own colour.
  it("tints only its own glyph, not icons inside the action", () => {
    const { container } = render(
      <EmptyState icon={Glyph} action={<button><svg data-testid="cta-icon" /></button>}>
        Nothing.
      </EmptyState>,
    );
    expect(screen.getByTestId("glyph").getAttribute("class")).toBe("empty-state-icon");
    expect(screen.getByTestId("cta-icon").getAttribute("class")).toBeNull();
    expect(container.querySelector(".empty-state")).toBeTruthy();
  });
});
