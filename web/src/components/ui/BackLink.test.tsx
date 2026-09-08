// SPDX-FileCopyrightText: 2026 Angels' Ware
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
    const { container } = mount("/runs", "Runs");
    const link = container.querySelector("a")!;
    expect(link.className).toBe("back-link");
    expect(link.getAttribute("style")).toBeNull();
  });

  it("keeps the arrow out of the accessible name", () => {
    mount("/support/queue", "Support queue");
    expect(screen.getByRole("link").textContent?.trim()).toBe("Support queue");
    expect(screen.getByRole("link", { name: "Support queue" })).toBeTruthy();
  });
});
