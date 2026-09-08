// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Routes, Route } from "react-router-dom";

vi.mock("react-i18next", () => {
  const catalog: Record<string, string> = {
    "relative.justNow": "just now",
    // The catalogue splits this into _one/_other; formatRelative passes the
    // base key, so the plural i18next would pick for these cases is inlined.
    "relative.minutesAgo": "{{count}} minutes ago",
    "runDetail.startedRelative": "Started {{when}}",
    "runDetail.queuedRelative": "Queued {{when}}",
  };
  const t = (k: string, o?: Record<string, unknown>) => {
    const s = catalog[k] ?? k;
    return o ? s.replace(/\{\{(\w+)\}\}/g, (_, n) => String(o[n] ?? "")) : s;
  };
  return {
    useTranslation: () => ({ t }),
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
vi.mock("../../auth", () => ({ useAuth: () => ({ token: "tok", hasPerm: () => true, me: {} }) }));

const getJob = vi.fn();
const listRunNodes = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    getJob: (...a: unknown[]) => getJob(...a),
    listRunNodes: (...a: unknown[]) => listRunNodes(...a),
    approveNode: vi.fn().mockResolvedValue({}),
    listRunLogs: vi.fn().mockResolvedValue({ entries: [] }),
    getGraph: vi.fn().mockResolvedValue(null),
    listDrops: vi.fn().mockResolvedValue({ drops: [] }),
  },
}));

import { RunDetail } from "./RunDetail";

const RUN_ID = "69a6f59b21aa3a4e7530df27";

function renderRun() {
  return render(
    <MemoryRouter initialEntries={[`/runs/${RUN_ID}`]}>
      <Routes>
        <Route path="/runs/:runID" element={<RunDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("RunDetail header", () => {
  it("titles the page with the flow, not the run id", async () => {
    getJob.mockResolvedValue({ ID: RUN_ID, GraphID: "refunds", Status: "succeeded" });
    listRunNodes.mockResolvedValue({ nodes: [] });
    const { container } = renderRun();

    const heading = await screen.findByRole("heading", { level: 1 });
    expect(heading).toHaveTextContent("refunds");
    expect(container.querySelector(".page-title")).not.toHaveTextContent(RUN_ID);
  });

  it("keeps the run id reachable, with a way to copy it", async () => {
    getJob.mockResolvedValue({ ID: RUN_ID, GraphID: "refunds", Status: "succeeded" });
    listRunNodes.mockResolvedValue({ nodes: [] });
    renderRun();

    await waitFor(() => expect(screen.getByText(RUN_ID)).toBeInTheDocument());
    expect(screen.getByText("runDetail.summaryRunId")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "runDetail.copyId" })).toBeInTheDocument();
  });
});

describe("RunDetail subtitle", () => {
  it("says how long ago the run started", async () => {
    const started = new Date(Date.now() - 5 * 60 * 1000).toISOString();
    getJob.mockResolvedValue({
      ID: RUN_ID, GraphID: "refunds", Status: "succeeded", StartedAt: started,
    });
    listRunNodes.mockResolvedValue({ nodes: [] });
    const { container } = renderRun();

    await screen.findByRole("heading", { level: 1 });
    const sub = container.querySelector(".page-title .sub");
    expect(sub).toHaveTextContent("Started 5 minutes ago");
    expect(sub?.getAttribute("title")).toBeTruthy();
  });

  it("says queued, not started, for a run that has not begun", async () => {
    const enqueued = new Date(Date.now() - 5 * 60 * 1000).toISOString();
    getJob.mockResolvedValue({
      ID: RUN_ID, GraphID: "refunds", Status: "queued", EnqueuedAt: enqueued,
    });
    listRunNodes.mockResolvedValue({ nodes: [] });
    const { container } = renderRun();

    await screen.findByRole("heading", { level: 1 });
    expect(container.querySelector(".page-title .sub")).toHaveTextContent("Queued 5 minutes ago");
  });

  it("drops the subtitle when the run carries no timestamp", async () => {
    getJob.mockResolvedValue({ ID: RUN_ID, GraphID: "refunds", Status: "succeeded" });
    listRunNodes.mockResolvedValue({ nodes: [] });
    const { container } = renderRun();

    await screen.findByRole("heading", { level: 1 });
    expect(container.querySelector(".page-title .sub")).toBeNull();
  });
});
