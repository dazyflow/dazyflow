// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

// Same stable-identity mocks as RunListLinks.test.tsx: RunList's load effect
// depends on `t` and `me`, so a fresh object per render re-fires it forever.
vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
vi.mock("../../auth", () => {
  const auth = {
    token: "tok",
    me: { tenant: "t", workspace: "ws" },
    activeTenant: "t",
    activeWorkspace: "ws",
    hasPerm: () => true,
  };
  return { useAuth: () => auth };
});

const listAllRuns = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listAllRuns: (...a: unknown[]) => listAllRuns(...a),
    listRuns: vi.fn().mockResolvedValue({ runs: [] }),
    listGraphs: vi.fn().mockResolvedValue({ graphs: [] }),
    retryRun: vi.fn().mockResolvedValue({ job_id: "x" }),
  },
}));

import { RunList } from "./RunList";

// An empty response means two completely different things, and both used to
// get the sentence "No runs match this filter." The status chip, the flow
// picker and the date range narrow the fetch SERVER-side; with none of them
// set, an empty response means the account has never run anything — and
// blaming a filter the person never touched is a poor first visit.
describe("RunList empty states", () => {
  beforeEach(() => {
    listAllRuns.mockReset();
    listAllRuns.mockResolvedValue({ runs: [] });
  });

  it("reads as a first-run state when no filter is set", async () => {
    render(
      <MemoryRouter initialEntries={["/runs"]}>
        <RunList />
      </MemoryRouter>,
    );
    expect(await screen.findByText("runList.emptyFirstTitle")).toBeTruthy();
    expect(screen.getByText("runList.emptyFirstBody")).toBeTruthy();
    // And a way out — the page that can actually produce a run.
    const cta = screen.getByRole("button", { name: "runList.emptyFirstCta" });
    expect(cta).toBeTruthy();
    // The filter wording must NOT appear: nothing was filtered.
    expect(screen.queryByText("runList.empty")).toBeNull();
  });

  it("blames the filter only when a filter is actually set", async () => {
    render(
      <MemoryRouter initialEntries={["/runs?status=failed"]}>
        <RunList />
      </MemoryRouter>,
    );
    await waitFor(() => expect(screen.getByText("runList.empty")).toBeTruthy());
    expect(screen.queryByText("runList.emptyFirstTitle")).toBeNull();
  });
});
