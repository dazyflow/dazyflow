// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Routes, Route } from "react-router-dom";

vi.mock("react-i18next", () => {
  // Only the strings these tests read back; everything else falls through as
  // its key, matching the simpler mock the other RunDetail tests use.
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
vi.mock("../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));
vi.mock("../auth", () => ({ useAuth: () => ({ token: "tok", hasPerm: () => true, me: {} }) }));

const getJob = vi.fn();
const listRunNodes = vi.fn();
vi.mock("../api", () => ({
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

// The page used to introduce itself as "Run 69a6f59b21aa3a4e7530df27" —
// twenty-four characters of hex directly under the title, which is the first
// thing a non-technical user read and the only thing they couldn't act on.
// Every other detail page uses that slot for a human subtitle. The id is still
// needed (support, dzctl, bug reports), so it moved down into the details card
// with the rest of the plumbing.
describe("RunDetail header", () => {
  it("titles the page with the flow, not the run id", async () => {
    getJob.mockResolvedValue({ ID: RUN_ID, GraphID: "refunds", Status: "succeeded" });
    listRunNodes.mockResolvedValue({ nodes: [] });
    const { container } = renderRun();

    const heading = await screen.findByRole("heading", { level: 1 });
    expect(heading).toHaveTextContent("refunds");
    // The whole title block, not just the h1: the id used to sit in the
    // subtitle line right beneath it, which is the actual complaint.
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

// The slot the hex id vacated now answers "which run is this?" the way a
// person actually asks it. The exact instant stays in the details card (and on
// hover), so the coarse label is orientation, not the record.
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
    // The precise timestamp stays one hover away.
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
