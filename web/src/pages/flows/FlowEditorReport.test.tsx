// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The editor's failure banner and the support surface, joined.
//
// "Contact support" on this banner used to be the operator-configured
// mailto/URL and nothing else — a channel outside the product, where the user
// retypes an error the app is already displaying and support opens the mail
// with no flow, no run and no diagnostic bundle. The ticket surface that
// solves all three existed; the banner just didn't reach it.
//
// What's pinned here is the JOIN, because every piece of it typechecks while
// being wrong: the wrong flow id, a stale run, a modal wired to a state flag
// nothing sets, or a prefill that silently arrives empty.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import {
  frame,
  installLayoutStubs,
  makeStreamJob,
  manifests,
  twoStepGraph,
} from "./editorTestHarness";

installLayoutStubs();

vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o.label === "string" ? `${k}:${o.label}` : k;
  const value = { t, i18n: { language: "en" } };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({
  default: { language: "en", t: (k: string) => k },
}));

// The one thing these tests vary: whether the deployment has the native
// ticket surface, and what operator contact is configured behind it.
const me: {
  subject: string;
  tenant: string;
  workspace: string;
  support_tickets_enabled?: boolean;
  support_contact?: string;
} = { subject: "a@b.c", tenant: "acme", workspace: "main" };

vi.mock("../../auth", () => ({
  useAuth: () => ({
    token: "tok",
    me,
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: () => true,
  }),
}));

const stream = makeStreamJob();
const runGraph = vi.fn();
const getNodeRecord = vi.fn();
const createTicket = vi.fn();
// Overridable so one test can load a flow that was never named.
const loadGraph = vi.fn();

vi.mock("../../api", () => {
  const statusOf = (e: unknown) => (e as { status?: number } | null)?.status;
  const codeOf = (e: unknown) => (e as { code?: string } | null)?.code;
  return {
    APIError: class extends Error {},
    isHTTPStatus: (e: unknown, status: number) => statusOf(e) === status,
    isErrorCode: (e: unknown, code: string) => codeOf(e) === code,
    api: {
      loadGraph: (...a: unknown[]) => loadGraph(...a),
      listDrops: () => Promise.resolve({ drops: manifests }),
      dropSuggestions: () => Promise.resolve([]),
      listSecrets: () => Promise.resolve({ secrets: [] }),
      getPublishedInfo: () => Promise.resolve({ published: false }),
      flowHistory: () => Promise.resolve({ revisions: [] }),
      saveGraph: () => Promise.resolve({}),
      streamJob: (...a: never[]) => stream.streamJob(...a),
      runGraph: (...a: unknown[]) => runGraph(...a),
      listRuns: () => Promise.resolve({ runs: [] }),
      getNodeRecord: (...a: unknown[]) => getNodeRecord(...a),
      createTicket: (...a: unknown[]) => createTicket(...a),
      retryRun: () => Promise.resolve({}),
      cancelRun: () => Promise.resolve({}),
      sampleNode: () => Promise.resolve({}),
      listProviders: () => Promise.resolve({ providers: [] }),
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

// The ticket thread is a real route here, not a stub, because filing is only
// half the job: the modal navigates on success, and a user left staring at the
// editor has no idea whether anything was sent.
function mount(id = "coffee-reorder") {
  return render(
    <MemoryRouter initialEntries={[`/flows/${id}`]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
        <Route path="/support/:id" element={<div>ticket-thread</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

async function emit(kind: string, data: unknown) {
  const sub = stream.latest();
  expect(sub, "no open run stream").toBeTruthy();
  await act(async () => {
    sub.emit(kind, data);
  });
}

// Run the flow and let one step fail, which is the only way this banner
// appears with a run behind it.
async function failARun() {
  getNodeRecord.mockResolvedValue({
    Result: { error: { message: "no topic configured" } },
  });
  mount();
  await userEvent.click(await screen.findByText("editor.run"));
  await waitFor(() => expect(stream.latest()?.runID).toBe("run-1"));
  await emit(...frame.node("ntfy_1", "failed"));
  await emit(...frame.terminal("failed"));
  return screen.findByText(/editor.runFailed/);
}

beforeEach(() => {
  vi.clearAllMocks();
  stream.subs.length = 0;
  loadGraph.mockImplementation((...a: unknown[]) =>
    Promise.resolve(twoStepGraph(String(a[3]))),
  );
  runGraph.mockResolvedValue({ job_id: "run-1" });
  createTicket.mockResolvedValue({ id: "tk-1" });
  me.support_tickets_enabled = true;
  me.support_contact = undefined;
});

describe("reporting a failure from the editor", () => {
  it("offers the in-app ticket rather than an outside channel", async () => {
    await failARun();
    expect(await screen.findByText("report.title")).toBeInTheDocument();
  });

  it("files the ticket against THIS flow and the run that failed", async () => {
    await failARun();
    await userEvent.click(await screen.findByText("report.title"));
    await userEvent.click(await screen.findByText("report.send"));

    await waitFor(() => expect(createTicket).toHaveBeenCalled());
    const [token, body] = createTicket.mock.calls[0];
    expect(token).toBe("tok");
    // The flow id is the route's, not a node's and not the graph's label:
    // getting this wrong attaches someone else's diagnostic bundle.
    expect(body.flow_id).toBe("coffee-reorder");
    // The run is what turns "here's my flow" into "here's how it broke".
    expect(body.run_id).toBe("run-1");
    // And the user is taken to the thread, so the ticket is something they
    // can see and add to rather than a form that appeared to do nothing.
    expect(await screen.findByText("ticket-thread")).toBeInTheDocument();
  });

  it("opens with the error already written in, not an empty box", async () => {
    // The whole reason people send blank reports: the error is on screen and
    // the form asks them to retype it.
    await failARun();
    await userEvent.click(await screen.findByText("report.title"));

    const box = (await screen.findByPlaceholderText(
      "report.messagePlaceholder",
    )) as HTMLTextAreaElement;
    // The error the banner is showing, and only that: the banner element also
    // contains the link that opened this dialog, and pulling in its label
    // would put "report.title" into the user's own message.
    expect(box.value).toMatch(/^editor\.runFailed/);
    expect(box.value).not.toContain("report.title");
  });

  it("fills the subject for an unnamed flow, so Send is not dead on arrival", async () => {
    // A flow created and never renamed carries no `name` — only its id. Send
    // is gated on a non-empty subject, so a prefill that skips it hands the
    // user a dialog they cannot submit. (Found exactly this way: the dialog
    // opened with the error written in and the button greyed out.)
    loadGraph.mockImplementation((...a: unknown[]) =>
      Promise.resolve({ ...twoStepGraph(String(a[3])), name: undefined }),
    );
    await failARun();
    await userEvent.click(await screen.findByText("report.title"));

    const send = await screen.findByText("report.send");
    expect(send.closest("button")).not.toBeDisabled();
  });

  it("sends what the user edited, not the prefill", async () => {
    // Prefill is a starting point. If the box were read-only — or the state
    // reset on submit — the user's own account of what they were doing, the
    // one thing the bundle cannot carry, would be dropped.
    await failARun();
    await userEvent.click(await screen.findByText("report.title"));
    const box = await screen.findByPlaceholderText("report.messagePlaceholder");
    await userEvent.clear(box);
    await userEvent.type(box, "it worked yesterday");
    await userEvent.click(await screen.findByText("report.send"));

    await waitFor(() => expect(createTicket).toHaveBeenCalled());
    expect(createTicket.mock.calls[0][1].message).toBe("it worked yesterday");
  });

  it("falls back to the operator's contact when tickets are off", async () => {
    me.support_tickets_enabled = false;
    me.support_contact = "help@acme.com";
    await failARun();

    expect(screen.queryByText("report.title")).toBeNull();
    const link = await screen.findByText("common.contactSupport");
    expect(link).toHaveAttribute("href", "mailto:help@acme.com");
  });

  it("offers nothing at all when neither is configured", async () => {
    // A dead "contact support" is worse than none: it invites a click that
    // does nothing and reads as the app being broken twice over.
    me.support_tickets_enabled = false;
    me.support_contact = undefined;
    await failARun();

    expect(screen.queryByText("report.title")).toBeNull();
    expect(screen.queryByText("common.contactSupport")).toBeNull();
  });
});
