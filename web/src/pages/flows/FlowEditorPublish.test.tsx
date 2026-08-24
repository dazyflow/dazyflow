// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Going live, pausing, and the draft-vs-live status behind the toolbar toggle.
//
// The rule worth pinning is draft safety: "Live" means published AND enabled,
// and resuming a flow that is already published must NOT re-publish it — the
// edits you made while it was paused stay a draft until you deliberately push
// them. One `if` expresses that, and nothing else in the codebase would notice
// if it inverted. Written so usePublish can be extracted the way useRunStream
// and useAutosave were: against tests rather than against hope.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { installLayoutStubs, makeStreamJob, manifests, twoStepGraph } from "./editorTestHarness";

installLayoutStubs();

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t, i18n: { language: "en" } };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));

let perms = new Set(["graph:edit", "graph:run", "graph:admin"]);
vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok",
    me: { subject: "a@b.c", tenant: "acme", workspace: "main" },
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: (p: string) => perms.has(p),
  }),
}));

const stream = makeStreamJob();
const getPublishedInfo = vi.fn();
const publishFlow = vi.fn();
const setFlowEnabled = vi.fn();
const loadGraph = vi.fn();
const flowHistory = vi.fn();

vi.mock("../../api", () => {
  class APIError extends Error {
    status: number;
    code?: string;
    constructor(status: number, msg?: string) {
      super(msg);
      this.status = status;
    }
  }
  const statusOf = (e: unknown) => (e as { status?: number } | null)?.status;
  const codeOf = (e: unknown) => (e as { code?: string } | null)?.code;
  return {
    APIError,
    isHTTPStatus: (e: unknown, s: number) => statusOf(e) === s,
    isErrorCode: (e: unknown, c: string) => codeOf(e) === c,
    api: {
      getPublishedInfo: (...a: unknown[]) => getPublishedInfo(...a),
      publishFlow: (...a: unknown[]) => publishFlow(...a),
      setFlowEnabled: (...a: unknown[]) => setFlowEnabled(...a),
      loadGraph: (...a: unknown[]) => loadGraph(...a),
      flowHistory: (...a: unknown[]) => flowHistory(...a),
      listDrops: () => Promise.resolve({ drops: manifests }),
      dropSuggestions: () => Promise.resolve([]),
      listSecrets: () => Promise.resolve({ secrets: [] }),
      listProviders: () => Promise.resolve({ providers: [] }),
      listRuns: () => Promise.resolve({ runs: [] }),
      saveGraph: () => Promise.resolve({}),
      streamJob: (...a: never[]) => stream.streamJob(...a),
      runGraph: () => Promise.resolve({ job_id: "run-1" }),
      getNodeRecord: () => Promise.resolve({ Result: { output: {} } }),
      retryRun: () => Promise.resolve({ job_id: "run-2" }),
      cancelRun: () => Promise.resolve({}),
      sampleNode: () => Promise.resolve({}),
      watchFlow: () => Promise.resolve({}),
      labelRevision: () => Promise.resolve({}),
      restoreFlow: () => Promise.resolve({}),
      deleteGraph: () => Promise.resolve({}),
      resetNodeState: () => Promise.resolve({}),
      resumeRun: () => Promise.resolve({}),
      approveNode: () => Promise.resolve({}),
      testTrigger: () => Promise.resolve({ job_id: "run-3" }),
    },
  };
});

// setLive checks `e instanceof APIError` directly rather than going through
// isHTTPStatus like the rest of the file, so this test needs a genuine instance
// of the mocked class — a plain object with a .status takes the wrong branch.
import { APIError } from "../../api";
import { FlowEditor } from "./FlowEditor";

function mount(id = "coffee-reorder") {
  return render(
    <MemoryRouter initialEntries={[`/flows/${id}`]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
      </Routes>
    </MemoryRouter>,
  );
}

// The toggle opens a confirm before doing anything, so every go-live/pause is
// two clicks: the toolbar switch, then the modal's action.
// The switch is found by ROLE because the go-live confirm button carries the
// same label as the toggle ("editor.goLive"), so matching on text alone finds
// two nodes once the dialog is open.
async function flipSwitch() {
  await userEvent.click(await screen.findByRole("switch"));
}
async function confirmIn(dialogRole: string, label: string) {
  const dialog = await screen.findByRole(dialogRole);
  await userEvent.click(await within(dialog).findByText(label));
}

beforeEach(() => {
  perms = new Set(["graph:edit", "graph:run", "graph:admin"]);
  stream.subs.length = 0;
  loadGraph.mockResolvedValue(twoStepGraph());
  flowHistory.mockResolvedValue({ revisions: [] });
  publishFlow.mockResolvedValue({});
  setFlowEnabled.mockResolvedValue({});
  getPublishedInfo.mockResolvedValue({ published: false });
});

describe("editor publish status", () => {
  it("offers Go live when nothing is published yet", async () => {
    mount();
    expect(await screen.findByText("editor.goLive")).toBeInTheDocument();
  });

  it("reads as Live once published and enabled", async () => {
    getPublishedInfo.mockResolvedValue({
      published: true,
      published_commit: "abc123",
      dirty: false,
    });
    mount();
    expect(await screen.findByText("editor.live")).toBeInTheDocument();
  });

  it("stays quiet when the status probe fails", async () => {
    // It's a status probe, not a user action: a failure means the pill doesn't
    // render, NOT an error banner across the canvas.
    getPublishedInfo.mockRejectedValue(new Error("boom"));
    mount();
    await screen.findByText("editor.run");
    expect(screen.queryByText(/editor.runFailed/)).not.toBeInTheDocument();
    expect(screen.queryByText(/loadFailed/)).not.toBeInTheDocument();
  });
});

describe("editor go live", () => {
  it("publishes and enables on a first go-live", async () => {
    getPublishedInfo.mockResolvedValue({ published: false });
    mount();
    await flipSwitch();
    await confirmIn("dialog", "editor.goLive");
    await waitFor(() => expect(publishFlow).toHaveBeenCalled());
  });

  // The draft-safety rule. A paused flow that already has a live version keeps
  // that version when you resume it; the edits made while paused stay a draft
  // until they are deliberately pushed. Re-publishing here would silently ship
  // work the user never chose to ship.
  it("resuming an already-published flow re-enables WITHOUT republishing", async () => {
    getPublishedInfo.mockResolvedValue({
      published: true,
      published_commit: "abc123",
      dirty: true,
    });
    loadGraph.mockResolvedValue({ ...twoStepGraph(), disabled: true });
    mount();
    await flipSwitch();
    await confirmIn("dialog", "editor.goLive");
    await waitFor(() =>
      expect(setFlowEnabled).toHaveBeenCalledWith(
        "tok",
        "acme",
        "main",
        "coffee-reorder",
        true,
      ),
    );
    expect(publishFlow).not.toHaveBeenCalled();
  });

  it("does not offer the switch at all without graph:admin", async () => {
    // Publishing arms the automatic triggers, so the control is gated on
    // graph:admin — the same bar the server enforces. An editor who can change
    // the flow still cannot decide what runs on a schedule.
    perms = new Set(["graph:edit", "graph:run"]);
    getPublishedInfo.mockResolvedValue({ published: false });
    mount();
    await screen.findByText("editor.run");
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    expect(screen.queryByText("editor.goLive")).not.toBeInTheDocument();
  });

  it("pausing disables the flow and never touches the published version", async () => {
    // The universal kill switch: it stops cron, poll, webhook and form triggers
    // alike, while leaving the live revision in place.
    getPublishedInfo.mockResolvedValue({
      published: true,
      published_commit: "abc123",
      dirty: false,
    });
    mount();
    await flipSwitch();
    await confirmIn("alertdialog", "editor.pause");
    await waitFor(() =>
      expect(setFlowEnabled).toHaveBeenCalledWith(
        "tok",
        "acme",
        "main",
        "coffee-reorder",
        false,
      ),
    );
    expect(publishFlow).not.toHaveBeenCalled();
  });

  it("tells you to save first when the flow has never been written", async () => {
    // A 404 from the enable endpoint means the flow doesn't exist server-side
    // yet — "couldn't pause" would be a lie about the cause.
    getPublishedInfo.mockResolvedValue({
      published: true,
      published_commit: "abc123",
      dirty: false,
    });
    setFlowEnabled.mockRejectedValue(new APIError(404, "no such flow"));
    mount();
    await flipSwitch();
    await confirmIn("alertdialog", "editor.pause");
    expect(await screen.findByText("editor.pauseSaveFirst")).toBeInTheDocument();
  });
});
