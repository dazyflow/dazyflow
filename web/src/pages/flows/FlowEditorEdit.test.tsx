// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Graph editing and undo/redo.
//
// lib/graphHistory.ts is unit-tested on its own, so what is left untested is
// the part that only exists inside the editor: WHEN a snapshot is taken, when
// it is refused, and what an undo means for the server.
//
// Three behaviours carry the weight:
//
//   An undo is an edit. applyHistoryDoc sets dirty, because otherwise the
//   server keeps holding the state the user just undid and autosave never
//   fires to correct it. These tests assert on the SAVED document rather than
//   on the canvas, which is both the thing that actually matters and the only
//   assertion jsdom can make honestly about a React Flow surface.
//
//   An undo is not itself undoable. The observer runs on every document change
//   including the one an undo causes; pendingHistoryApplyRef is what stops it
//   recording that as a fresh edit, which would clear the redo stack and strand
//   the user one step from where they were.
//
//   The stack is fenced. Undo must not reach past a document the user did not
//   author — a revision preview, a restore, an external edit arriving over the
//   flow watch. Undoing past someone else's change would silently clobber it.

import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
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

// Permissions are per-test here: "undo is unavailable to a viewer" is one of
// the behaviours under test, so hasPerm cannot be a constant.
const perms = { canEdit: true };
vi.mock("../../auth", () => {
  const auth = {
    token: "tok",
    me: { subject: "a@b.c", tenant: "acme", workspace: "main" },
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: (p: string) => (p === "graph:edit" ? perms.canEdit : true),
  };
  return { useAuth: () => auth };
});

const stream = makeStreamJob();
const loadGraph = vi.fn();
const saveGraph = vi.fn();
const listRuns = vi.fn();

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
    isHTTPStatus: (e: unknown, status: number) => statusOf(e) === status,
    isErrorCode: (e: unknown, code: string) => codeOf(e) === code,
    api: {
      loadGraph: (...a: unknown[]) => loadGraph(...a),
      saveGraph: (...a: unknown[]) => saveGraph(...a),
      listRuns: (...a: unknown[]) => listRuns(...a),
      listDrops: () => Promise.resolve({ drops: manifests }),
      dropSuggestions: () => Promise.resolve([]),
      listSecrets: () => Promise.resolve({ secrets: [] }),
      listProviders: () => Promise.resolve({ providers: [] }),
      getPublishedInfo: () => Promise.resolve({ published: false }),
      flowHistory: () => Promise.resolve({ revisions: [] }),
      streamJob: (...a: never[]) => stream.streamJob(...a),
      runGraph: () => Promise.resolve({ job_id: "run-1" }),
      getNodeRecord: () => Promise.resolve({ Result: { output: {} } }),
      retryRun: () => Promise.resolve({ job_id: "run-2" }),
      cancelRun: () => Promise.resolve({}),
      sampleNode: () => Promise.resolve({}),
      watchFlow: () => Promise.resolve({}),
      publishFlow: () => Promise.resolve({}),
      labelRevision: () => Promise.resolve({}),
      restoreFlow: () => Promise.resolve({}),
      deleteGraph: () => Promise.resolve({}),
      setFlowEnabled: () => Promise.resolve({}),
      resetNodeState: () => Promise.resolve({}),
      resumeRun: () => Promise.resolve({}),
      approveNode: () => Promise.resolve({}),
    },
  };
});

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

// Adding a frame is the one document edit reachable from the toolbar without
// touching the canvas — React Flow's pointer surface does not exist in jsdom.
const undoButton = () => screen.getByLabelText("editor.undo");
const redoButton = () => screen.getByLabelText("editor.redo");
const addFrame = () => screen.getByLabelText("editor.addFrame");

// The load is async and the autosave timer only arms once it resolves, so
// waiting for the toolbar first is load-bearing (see FlowEditorSave).
async function ready() {
  await screen.findByText("editor.run");
}
async function settle() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
}

// The frame count in the most recent PUT — the document as the server sees it.
function savedFrames(): number | undefined {
  const call = saveGraph.mock.calls.at(-1);
  const doc = call?.find((a) => a && typeof a === "object" && "nodes" in a) as
    | { frames?: unknown[] }
    | undefined;
  return doc && (doc.frames?.length ?? 0);
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  stream.subs.length = 0;
  perms.canEdit = true;
  loadGraph.mockResolvedValue(twoStepGraph());
  saveGraph.mockResolvedValue({});
  listRuns.mockResolvedValue({ runs: [] });
});

afterEach(() => {
  vi.useRealTimers();
  saveGraph.mockReset();
});

describe("editor undo/redo", () => {
  // A flow you have only just opened offers nothing to undo.
  //
  // What holds this is `record` treating its first observation as a baseline
  // rather than an edit — not the editor's history fence, which was checked by
  // deleting it and watching this test still pass. The fence earns its keep on
  // a MID-SESSION replacement, where a stack already exists; that case lives in
  // FlowEditorRevisions.test.tsx.
  it("offers nothing to undo on a freshly loaded flow", async () => {
    mount();
    await ready();
    await settle();
    expect(undoButton()).toBeDisabled();
    expect(redoButton()).toBeDisabled();
  });

  it("records an edit, and undoing reverses it in the saved document", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    mount();
    await ready();
    await settle();

    await user.click(addFrame());
    await settle();
    await waitFor(() => expect(savedFrames()).toBe(1));
    expect(undoButton()).toBeEnabled();

    await user.click(undoButton());
    await settle();
    // The assertion that matters: not merely that the canvas changed, but that
    // the server was told. An undo that left the frame on the server would be
    // the bug applyHistoryDoc's setDirty exists to prevent.
    await waitFor(() => expect(savedFrames()).toBe(0));
  });

  it("redoes what it undid", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    mount();
    await ready();
    await settle();

    await user.click(addFrame());
    await settle();
    await user.click(undoButton());
    await settle();
    await waitFor(() => expect(savedFrames()).toBe(0));

    expect(redoButton()).toBeEnabled();
    await user.click(redoButton());
    await settle();
    await waitFor(() => expect(savedFrames()).toBe(1));
  });

  // The observer fires again on the document change an undo causes. If that
  // were recorded as a fresh edit it would clear the redo stack, leaving the
  // user one step back with no way forward.
  //
  // Two things stop it, and it is worth being precise about which: the
  // re-observed document is identical to the history head, so `record`
  // classifies the delta as "none" and returns the state untouched.
  // pendingHistoryApplyRef in the editor is a second line of defence — deleting
  // it does NOT break this test, which was checked. What is pinned here is the
  // behaviour a user feels, not either mechanism.
  it("leaves the redo stack intact after an undo", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    mount();
    await ready();
    await settle();

    await user.click(addFrame());
    await settle();
    await user.click(undoButton());
    await settle();

    expect(redoButton()).toBeEnabled();
  });

  // A run executes the SAVED graph, so the editor holds the document still
  // while one is in flight — undo included.
  //
  // Mounted with the run already active rather than starting one mid-test: the
  // lock poll only runs WHILE locked ("no idle polling cost"), so an editor
  // that is not locked never notices a run someone else started. That is the
  // real behaviour, and a test that pretended otherwise would have been
  // asserting a poll that does not exist.
  it("is unavailable while a run holds the edit lock", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "running" }] });
    mount();
    await ready();
    await settle();

    await user.click(addFrame());
    await settle();
    // The edit is recorded — the fence is the lock, not a missing snapshot.
    expect(undoButton()).toBeDisabled();

    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "succeeded" }] });
    await settle();
    await settle();
    await waitFor(() => expect(undoButton()).toBeEnabled());
  });

  // The edit comes first on purpose. Asserting "disabled" on a fresh mount
  // would have passed with the permission check deleted, because an empty
  // stack disables the button anyway — the test would have been measuring the
  // wrong thing. Recording an edit first makes the permission check the only
  // reason left for the button to be off.
  it("is unavailable without permission to edit the graph", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    perms.canEdit = false;
    mount();
    await ready();
    await settle();

    await user.click(addFrame());
    await settle();
    await user.click(addFrame());
    await settle();

    expect(undoButton()).toBeDisabled();
  });

  // Cmd/Ctrl+Z is muscle memory; the toolbar button is the discoverable copy.
  it("undoes from the keyboard", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    mount();
    await ready();
    await settle();

    await user.click(addFrame());
    await settle();
    await waitFor(() => expect(savedFrames()).toBe(1));

    await user.keyboard("{Control>}z{/Control}");
    await settle();
    await waitFor(() => expect(savedFrames()).toBe(0));
  });
});
