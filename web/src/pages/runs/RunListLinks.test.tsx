// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

// Stable `t` and a stable useTranslation result: RunList's load effect lists
// `t` in its deps, so a fresh function per render re-fires it forever. The
// real i18next hands back a stable one.
vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
// One stable object for the whole test: RunList's effects depend on `me`, so a
// fresh identity per render re-fires them and the component never settles.
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
const listGraphs = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listAllRuns: (...a: unknown[]) => listAllRuns(...a),
    listRuns: vi.fn().mockResolvedValue({ runs: [] }),
    listGraphs: (...a: unknown[]) => listGraphs(...a),
    retryRun: vi.fn().mockResolvedValue({ job_id: "x" }),
  },
}));

import { RunList } from "./RunList";

const RUN_ID = "69a6f59b21aa3a4e7530df27";
const FLOW_ID = "refunds";

// A list of runs has to lead to runs. The row's obvious target used to open the
// EDITOR — a different object, on a page you go to when you want to change the
// flow, not when you want to know what a run did — while the run itself was
// parked behind a muted 14px glyph at the end of the row. The dashboard's
// recent-runs lists always linked straight to the run; this is the page that
// disagreed.
describe("RunList row links", () => {
  beforeEach(() => {
    listGraphs.mockResolvedValue({ graphs: [{ id: FLOW_ID, name: "Refunds" }] });
    listAllRuns.mockResolvedValue({
      runs: [{
        id: RUN_ID, graph_id: FLOW_ID, status: "succeeded",
        enqueued_at: "2026-08-24T09:00:00Z", started_at: "2026-08-24T09:00:00Z",
        finished_at: "2026-08-24T09:00:04Z",
      }],
    });
  });

  it("points the flow-name link at the run, not the editor", async () => {
    render(<MemoryRouter><RunList /></MemoryRouter>);
    const name = await screen.findByRole("link", { name: /Refunds/ });
    expect(name).toHaveAttribute("href", `/runs/${RUN_ID}`);
  });

  // The hit area is CSS: .run-name-cell drops the td's padding and the link
  // takes it back, so the link fills the cell. jsdom does no layout, so what's
  // worth pinning is the structure that rule selects on — an inline style
  // creeping back onto the link would quietly shrink the target to the text.
  it("gives the name link the whole cell to fill", async () => {
    const { container } = render(<MemoryRouter><RunList /></MemoryRouter>);
    const name = await screen.findByRole("link", { name: /Refunds/ });
    const cell = container.querySelector("td.run-name-cell");
    expect(cell).not.toBeNull();
    expect(name.parentElement).toBe(cell);
    expect(name.getAttribute("style")).toBeNull();
  });

  it("keeps the editor reachable as the row's secondary action", async () => {
    render(<MemoryRouter><RunList /></MemoryRouter>);
    await waitFor(() => expect(listAllRuns).toHaveBeenCalled());
    const edit = await screen.findByRole("link", { name: "common.openInEditor" });
    expect(edit).toHaveAttribute("href", `/flows/${FLOW_ID}?run=${RUN_ID}`);
  });
});
