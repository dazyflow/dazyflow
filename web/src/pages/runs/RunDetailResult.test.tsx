// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The Result and Files panels, through the page that mounts them.
//
// The panels are what a manually-run flow is FOR: the run produced an answer,
// and until it is on this page the only way to read it was to expand a step
// and then a port. Two shapes have to survive: a rows value becomes a table
// (not JSON), and a value that is a file becomes a download.

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Routes, Route } from "react-router-dom";

vi.mock("react-i18next", () => {
  const catalog: Record<string, string> = {
    "runDetail.result": "Result",
    "runDetail.resultFrom": "From “{{label}}”",
    "runDetail.files": "Files",
    "runDetail.fileFrom": "from “{{label}}”",
    "runDetail.fileTemporary": "temporary — not kept after the run",
    "common.download": "Download",
    "common.downloadCsv": "Download CSV",
    "runDetail.resultRows_other": "{{count}} rows",
    "runDetail.resultRows": "{{count}} rows",
    "common.copy": "Copy",
    "common.copied": "Copied",
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
vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok",
    hasPerm: () => true,
    me: { tenant: "acme", workspace: "main" },
    activeTenant: "acme",
    activeWorkspace: "main",
  }),
}));

const getJob = vi.fn();
const listRunNodes = vi.fn();
const downloadWorkspaceFile = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    getJob: (...a: unknown[]) => getJob(...a),
    listRunNodes: (...a: unknown[]) => listRunNodes(...a),
    downloadWorkspaceFile: (...a: unknown[]) => downloadWorkspaceFile(...a),
    approveNode: vi.fn().mockResolvedValue({}),
    listRunLogs: vi.fn().mockResolvedValue({ entries: [] }),
    getGraph: vi.fn().mockResolvedValue(null),
    loadGraph: vi.fn().mockResolvedValue(null),
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

function succeededRun() {
  getJob.mockResolvedValue({
    ID: RUN_ID,
    GraphID: "weekly-report",
    Status: "succeeded",
  });
}

describe("RunDetail result panel", () => {
  it("renders a rows result as a table, not JSON", async () => {
    succeededRun();
    listRunNodes.mockResolvedValue({
      nodes: [
        {
          ID: "j1",
          NodeID: "count",
          Status: "succeeded",
          Result: {
            output: {
              rows: {
                data: [
                  { region: "North", orders: 42 },
                  { region: "South", orders: 7 },
                ],
                headers: ["region", "orders"],
              },
            },
          },
        },
      ],
    });
    renderRun();

    // The header row is the data's own column names.
    await waitFor(() =>
      expect(screen.getByRole("columnheader", { name: "region" })).toBeTruthy(),
    );
    expect(screen.getByRole("columnheader", { name: "orders" })).toBeTruthy();
    // And the values are cells, not a JSON blob.
    expect(screen.getByRole("cell", { name: "North" })).toBeTruthy();
    expect(screen.getByRole("cell", { name: "42" })).toBeTruthy();
    expect(document.body.textContent).not.toContain('"region":');
  });

  it("offers the rows result as a CSV download", async () => {
    succeededRun();
    listRunNodes.mockResolvedValue({
      nodes: [
        {
          ID: "j1",
          NodeID: "count",
          Status: "succeeded",
          Result: { output: { rows: { data: [{ region: "North" }] } } },
        },
      ],
    });
    renderRun();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Download CSV/ })).toBeTruthy(),
    );
  });

  it("still shows a text result as text", async () => {
    succeededRun();
    listRunNodes.mockResolvedValue({
      nodes: [
        {
          ID: "j1",
          NodeID: "summarize",
          Status: "succeeded",
          Result: { output: { text: { data: "42 orders this week" } } },
        },
      ],
    });
    renderRun();
    await waitFor(() =>
      expect(screen.getByText("42 orders this week")).toBeTruthy(),
    );
    expect(screen.queryByRole("columnheader")).toBeNull();
  });
});

describe("RunDetail files panel", () => {
  it("lists a file a step wrote and downloads it", async () => {
    succeededRun();
    listRunNodes.mockResolvedValue({
      nodes: [
        {
          ID: "j1",
          NodeID: "save",
          Status: "succeeded",
          Result: {
            output: { out: { ref: "reports/week.csv", mime: "text/csv" } },
          },
        },
      ],
    });
    downloadWorkspaceFile.mockResolvedValue(new Blob(["a,b\n1,2"]));
    renderRun();

    await waitFor(() => expect(screen.getByText("week.csv")).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: /Download/ }));
    await waitFor(() =>
      expect(downloadWorkspaceFile).toHaveBeenCalledWith(
        "tok",
        "acme",
        "main",
        "reports/week.csv",
      ),
    );
  });

  // The scratch tree is reclaimed when the run ends, and the endpoint refuses
  // it — so the row says why there is nothing to fetch instead of offering a
  // button that fails.
  it("marks a scratch file as temporary with no download", async () => {
    succeededRun();
    listRunNodes.mockResolvedValue({
      nodes: [
        {
          ID: "j1",
          NodeID: "stage",
          Status: "succeeded",
          Result: { output: { out: { ref: "scratch://tmp/payload.bin" } } },
        },
      ],
    });
    renderRun();

    await waitFor(() =>
      expect(screen.getByText(/temporary/)).toBeTruthy(),
    );
    expect(screen.queryByRole("button", { name: /^Download$/ })).toBeNull();
  });

  it("shows no Files heading when the run wrote none", async () => {
    succeededRun();
    listRunNodes.mockResolvedValue({
      nodes: [
        {
          ID: "j1",
          NodeID: "summarize",
          Status: "succeeded",
          Result: { output: { text: { data: "done" } } },
        },
      ],
    });
    renderRun();
    await waitFor(() => expect(screen.getByText("done")).toBeTruthy());
    expect(screen.queryByRole("heading", { name: "Files" })).toBeNull();
  });
});
