// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { BackLink } from "./BackLink";

function mount(to: string, label: string) {
  return render(
    <MemoryRouter>
      <BackLink to={to} label={label} />
    </MemoryRouter>,
  );
}

describe("BackLink", () => {
  it("renders a link to the parent, named by the parent", () => {
    mount("/admin/platform/orgs", "Organizations");
    const link = screen.getByRole("link", { name: "Organizations" });
    expect(link).toHaveAttribute("href", "/admin/platform/orgs");
  });

  it("carries the shared class and no inline style overrides", () => {
    // The whole point of consolidating: two admin pages used to re-declare
    // .back-link's own properties inline, hardcoding gap: 4 and a --space-2
    // margin against the class's var(--space-1) — so the two deepest pages in
    // the app sat at a different vertical rhythm from every other detail page.
    const { container } = mount("/runs", "Runs");
    const link = container.querySelector("a")!;
    expect(link.className).toBe("back-link");
    expect(link.getAttribute("style")).toBeNull();
  });

  it("keeps the arrow out of the accessible name", () => {
    // The icon is decoration; the label already says where you land. A screen
    // reader announcing "Organizations, link" is useful — the bare "Back" this
    // replaced was not.
    mount("/support/queue", "Support queue");
    expect(screen.getByRole("link").textContent?.trim()).toBe("Support queue");
    expect(screen.getByRole("link", { name: "Support queue" })).toBeTruthy();
  });
});
