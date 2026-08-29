// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

// Same stable-identity mocks as the other RunList tests: the load effect
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

// The dashboard's stat tiles are the main way into this page, and each one
// counts by a filter — "Runs today" by a day, "Needs attention" by status. A
// tile that dropped you on the unfiltered list showed a different number than
// the one you clicked. ?status= was already honoured; ?since=/?until= are the
// date half of the same contract.
describe("RunList deep links", () => {
  beforeEach(() => {
    listAllRuns.mockReset();
    listAllRuns.mockResolvedValue({ runs: [] });
  });

  it("pre-fills the date inputs from ?since=/?until=", async () => {
    render(
      <MemoryRouter initialEntries={["/runs?since=2026-08-29&until=2026-08-29"]}>
        <RunList />
      </MemoryRouter>,
    );
    const from = (await screen.findByLabelText(
      "runList.filterFrom",
    )) as HTMLInputElement;
    const to = screen.getByLabelText("runList.filterTo") as HTMLInputElement;
    expect(from.value).toBe("2026-08-29");
    expect(to.value).toBe("2026-08-29");
  });

  it("bounds the fetch to that local day", async () => {
    render(
      <MemoryRouter initialEntries={["/runs?since=2026-08-29&until=2026-08-29"]}>
        <RunList />
      </MemoryRouter>,
    );
    await waitFor(() => expect(listAllRuns).toHaveBeenCalled());
    const opts = listAllRuns.mock.calls[0][1];
    // Local midnight of the selected day, through local midnight of the next
    // one — the whole day, in the viewer's timezone.
    expect(opts.since).toBe(new Date(2026, 7, 29).toISOString());
    expect(opts.until).toBe(new Date(2026, 7, 30).toISOString());
  });

  it("ignores a param that isn't a calendar date", async () => {
    render(
      <MemoryRouter initialEntries={["/runs?since=yesterday"]}>
        <RunList />
      </MemoryRouter>,
    );
    const from = (await screen.findByLabelText(
      "runList.filterFrom",
    )) as HTMLInputElement;
    expect(from.value).toBe("");
    await waitFor(() => expect(listAllRuns).toHaveBeenCalled());
    expect(listAllRuns.mock.calls[0][1].since).toBeUndefined();
    // No filter was applied, so an empty response is a first-run state, not a
    // filter that matched nothing.
    expect(await screen.findByText("runList.emptyFirstTitle")).toBeTruthy();
  });
});
