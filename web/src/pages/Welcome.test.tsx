// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guards on the Welcome page's STRUCTURE, which is the thing the rework
// changed and the thing nothing else would catch. Copy can be rewritten
// freely — every assertion here is on order, destination, or presence.
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
vi.mock("../auth", () => ({
  useAuth: () => ({
    me: { tenant: "acme", subject: "u1" },
    token: "tok",
    activeTenant: "acme",
    activeWorkspace: "",
  }),
}));
vi.mock("../api", () => ({ api: { listGraphs: vi.fn() } }));

const loadRecentFlow = vi.fn();
vi.mock("../recentFlow", () => ({
  loadRecentFlow: (...a: unknown[]) => loadRecentFlow(...a),
  userScope: () => "acme:u1",
}));

import { Welcome } from "./Welcome";

const HAS_FLOWS = "dazyflow.hasFlows.acme:u1";

function mount() {
  return render(
    <MemoryRouter>
      <Welcome />
    </MemoryRouter>,
  );
}

describe("Welcome", () => {
  beforeEach(() => {
    loadRecentFlow.mockReset().mockReturnValue(null);
    localStorage.clear();
  });

  it("explains itself before offering anything to click", () => {
    const { container } = mount();
    const intro = container.querySelector(".welcome-intro")!;
    const firstLink = container.querySelector("a")!;
    // The bug this replaces: the intro sat below the resume link, so the line
    // describing the page arrived after the reader had already been handed an
    // action.
    expect(
      intro.compareDocumentPosition(firstLink) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("offers the three new-flow routes at equal weight, in tab order", () => {
    const { container } = mount();
    const options = [...container.querySelectorAll(".welcome-option")];
    expect(options.map((a) => a.getAttribute("href"))).toEqual([
      "/flows/new?tab=template",
      "/flows/new?tab=ai",
      "/flows/new?tab=blank",
    ]);
    // Equal weight means one shared class and no per-option modifier — the old
    // page ranked these as a button, two text links and a boxed card.
    for (const o of options) {
      expect(o.className).toBe("welcome-option");
    }
  });

  it("describes what each route does, not just its name", () => {
    const { container } = mount();
    for (const o of container.querySelectorAll(".welcome-option")) {
      expect(within(o as HTMLElement).getByText(/option\..*\.desc/)).toBeInTheDocument();
    }
  });

  it("features the zero-setup demo on a first run", () => {
    const { container } = mount();
    const demo = container.querySelector(".welcome-demo")!;
    expect(demo).toBeTruthy();
    expect(demo.getAttribute("href")).toBe("/flows/new?tab=template&start=try-it-now");
    // The sentence saying what the click does must be inside the click target,
    // not stranded under it.
    expect(within(demo as HTMLElement).getByText("welcome.demoDesc")).toBeInTheDocument();
  });

  it("drops the demo once the user has a flow to resume", () => {
    loadRecentFlow.mockReturnValue({ id: "f1", name: "Invoice watcher" });
    const { container } = mount();
    expect(container.querySelector(".welcome-resume")).toBeTruthy();
    // A demonstration outranking the user's own work is the ordering error
    // this prevents.
    expect(container.querySelector(".welcome-demo")).toBeNull();
  });

  it("drops the demo for a returning user with no recent flow", () => {
    localStorage.setItem(HAS_FLOWS, "1");
    const { container } = mount();
    expect(container.querySelector(".welcome-demo")).toBeNull();
    expect(screen.getByText("welcome.titleReturning")).toBeInTheDocument();
  });

  it("puts the resume link above every generic call to action", () => {
    loadRecentFlow.mockReturnValue({ id: "f1", name: "Invoice watcher" });
    const { container } = mount();
    const resume = container.querySelector(".welcome-resume")!;
    const firstOption = container.querySelector(".welcome-option")!;
    expect(
      resume.compareDocumentPosition(firstOption) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(resume.getAttribute("href")).toBe("/flows/f1");
  });
});
