// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
vi.mock("../auth", () => {
  const auth = {
    token: "tok",
    me: { subject: "ops@example.com", tenant: "t", workspace: "ws" },
    activeTenant: "t",
    activeWorkspace: "ws",
    hasPerm: () => true,
  };
  return { useAuth: () => auth };
});
vi.mock("../api", () => ({
  APIError: class extends Error {},
  api: {
    listAllRuns: vi.fn().mockResolvedValue({ runs: [] }),
    listGraphs: vi.fn().mockResolvedValue({ graphs: [] }),
    countPendingApprovals: vi.fn().mockResolvedValue({ count: 0 }),
  },
}));

import { Dashboard } from "./Dashboard";
import { formatDate } from "../lib/datetime";

describe("Dashboard stat tiles", () => {
  const href = (container: HTMLElement, label: string) =>
    (
      within(container).getByText(label).closest("a") as HTMLAnchorElement
    ).getAttribute("href");

  it("links each tile to the runs it counted", async () => {
    const { container } = render(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>,
    );
    const tiles = container.querySelector(".dash-stats") as HTMLElement;
    await within(tiles).findAllByText("0");
    const today = formatDate(new Date());
    expect(href(tiles, "dashboard.runsToday")).toBe(
      `/runs?since=${today}&until=${today}`,
    );
    expect(href(tiles, "dashboard.successRate")).toBe("/runs?status=succeeded");
    expect(href(tiles, "dashboard.needsAttention")).toBe("/runs?status=failed");
    expect(href(tiles, "dashboard.approvalsWaiting")).toBe("/approvals");
  });
});
