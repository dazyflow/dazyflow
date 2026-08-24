// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const tStub = (k: string, vars?: Record<string, unknown>) =>
  vars ? `${k}:${JSON.stringify(vars)}` : k;
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: tStub }),
}));

const navigate = vi.fn();
vi.mock("react-router-dom", () => ({
  useNavigate: () => navigate,
}));

let perms: string[] = [];
vi.mock("../auth", () => ({
  useAuth: () => ({ hasPerm: (p: string) => perms.includes(p) }),
}));
// FlowIcon is stubbed to keep the assertions about rows, not glyphs. ICON is
// the real scale — it's plain data, and the component reads it while building
// the row list, so a stub would just be a second copy to keep in sync.
vi.mock("../icons", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../icons")>()),
  FlowIcon: () => <span data-testid="flow-icon" />,
}));

import { CommandPalette } from "./CommandPalette";
import type { FlowSummary } from "../types";

const flows = [
  { id: "invoice-sync", name: "Invoice sync" },
  { id: "alerts", name: "Alerts" },
] as FlowSummary[];

function open(query?: string) {
  const view = render(
    <CommandPalette open onClose={() => {}} flows={flows} />,
  );
  return { ...view, query };
}

async function search(q: string) {
  await userEvent.type(screen.getByRole("textbox"), q);
}

function rowNames(): string[] {
  return screen
    .getAllByRole("option")
    .map((el) => el.querySelector(".quick-palette-row-name")?.textContent ?? "");
}

describe("CommandPalette", () => {
  beforeEach(() => {
    navigate.mockReset();
    perms = [];
  });

  it("lists workspace destinations and flows", () => {
    open();
    const names = rowNames();
    expect(names).toContain("common.flows");
    expect(names).toContain("nav.runs");
    expect(names).toContain("Invoice sync");
  });

  // Admin destinations were absent entirely, so everything configured rather
  // than browsed — credentials, secrets, the git mirror — was unreachable
  // from the launcher.
  it("hides admin destinations without the permission", () => {
    open();
    expect(rowNames()).not.toContain("admin.cardGitTitle");
    // Account settings are personal, so they stay available to everyone.
    expect(rowNames()).toContain("commandPalette.account");
  });

  it("offers admin destinations to an org admin", () => {
    perms = ["organization:admin"];
    open();
    const names = rowNames();
    expect(names).toContain("admin.cardGitTitle");
    expect(names).toContain("admin.cardUsersTitle");
    expect(names).toContain("admin.cardAuditTitle");
  });

  it("offers them to a flow admin too, matching the Admin index's own bar", () => {
    perms = ["graph:admin"];
    open();
    expect(rowNames()).toContain("admin.cardGitTitle");
  });

  // The point of the keywords: someone wanting to back their flows up types
  // "backup" or "mirror", not "git credentials".
  it.each(["mirror", "backup", "sync", "deploy key", "github"])(
    "finds the git mirror page by searching %j",
    async (term) => {
      perms = ["organization:admin"];
      open();
      await search(term);
      expect(rowNames()).toContain("admin.cardGitTitle");
    },
  );

  it("finds it in Swedish too", async () => {
    perms = ["organization:admin"];
    open();
    await search("spegling");
    expect(rowNames()).toContain("admin.cardGitTitle");
  });

  it("matches run history by a word that isn't in the label", async () => {
    open();
    await search("logs");
    expect(rowNames()).toContain("nav.runs");
  });

  it("keeps keywords out of the rendered rows", () => {
    perms = ["organization:admin"];
    open();
    // A row shows its label (and any sublabel) — never the search aliases,
    // which would read as noise.
    expect(screen.queryByText(/spegling/)).not.toBeInTheDocument();
    expect(screen.queryByText(/deploy key/)).not.toBeInTheDocument();
  });

  it("still matches flows by name", async () => {
    open();
    await search("invoice");
    expect(rowNames()).toEqual(["Invoice sync"]);
  });

  it("navigates on Enter", async () => {
    perms = ["organization:admin"];
    open();
    await search("mirror");
    await userEvent.keyboard("{Enter}");
    expect(navigate).toHaveBeenCalledWith("/admin/git-credentials");
  });

  // Three groups now, where the old code had two hardcoded checks — one for
  // "index 0 is nav" and one for the first flow row. A third group would have
  // rendered with no heading at all.
  it("renders a heading for every group present", () => {
    perms = ["organization:admin"];
    open();
    const headings = screen
      .getAllByText(
        (_, el) => el?.className === "quick-palette-group",
        { selector: ".quick-palette-group" },
      )
      .map((el) => el.textContent);
    expect(headings).toEqual([
      "commandPalette.goTo",
      "commandPalette.settingsGroup",
      "common.flows",
    ]);
  });

  it("drops the heading of a group filtered out entirely", async () => {
    perms = ["organization:admin"];
    open();
    await search("invoice");
    const headings = screen
      .getAllByText(
        (_, el) => el?.className === "quick-palette-group",
        { selector: ".quick-palette-group" },
      )
      .map((el) => el.textContent);
    expect(headings).toEqual(["common.flows"]);
  });

  it("reports no results for a query that matches nothing", async () => {
    open();
    await search("zzzznope");
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText(/commandPalette\.noResults/)).toBeInTheDocument();
  });
});
