// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Firing ONE trigger from its own card.
//
// A Slack-triggered flow could not be tested in the editor at all: the toolbar
// only offers "Send test event" for the webhook family, and plain Run makes
// slack_on_mention execute standalone, which fails with no_trigger_data by
// design. So the button lives on the trigger card, and the node id travels with
// the payload — a flow with two triggers cannot say which one a single toolbar
// button means.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { installLayoutStubs, makeStreamJob } from "./editorTestHarness";

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
vi.mock("../../auth", () => {
  const auth = {
    token: "tok",
    me: { subject: "a@b.c", tenant: "acme", workspace: "main" },
    activeTenant: "acme",
    activeWorkspace: "main",
    hasPerm: () => true,
  };
  return { useAuth: () => auth };
});

const stream = makeStreamJob();
const loadGraph = vi.fn();
const testTrigger = vi.fn();

const fireManifests = [
  {
    id: "slack_on_mention",
    label: "Slack",
    category: "trigger",
    inputs: [],
    outputs: [
      { port: "text", label: "Message" },
      { port: "channel", label: "Channel" },
    ],
    params_schema: { type: "object", properties: {} },
  },
  {
    id: "cron_trigger",
    label: "Schedule",
    category: "trigger",
    inputs: [],
    outputs: [{ port: "fired_at", label: "Fired at" }],
    params_schema: { type: "object", properties: {} },
  },
  {
    id: "ntfy",
    label: "Send notification",
    category: "notify",
    inputs: [{ port: "in", label: "In" }],
    outputs: [{ port: "out", label: "Out" }],
    params_schema: {
      type: "object",
      properties: { topic: { type: "string", title: "Topic" } },
      required: ["topic"],
    },
  },
];

vi.mock("../../api", () => {
  class APIError extends Error {
    status: number;
    constructor(status: number, msg?: string) {
      super(msg);
      this.status = status;
    }
  }
  const statusOf = (e: unknown) => (e as { status?: number } | null)?.status;
  return {
    APIError,
    isHTTPStatus: (e: unknown, status: number) => statusOf(e) === status,
    isErrorCode: () => false,
    api: {
      loadGraph: (...a: unknown[]) => loadGraph(...a),
      saveGraph: () => Promise.resolve({ commit: "c1" }),
      listRuns: () => Promise.resolve({ runs: [] }),
      listDrops: () => Promise.resolve({ drops: fireManifests }),
      dropSuggestions: () => Promise.resolve([]),
      listSecrets: () => Promise.resolve({ secrets: [] }),
      listProviders: () => Promise.resolve({ providers: [] }),
      getPublishedInfo: () => Promise.resolve({ published: true }),
      flowHistory: () => Promise.resolve({ revisions: [] }),
      streamJob: (...a: Parameters<typeof stream.streamJob>) => stream.streamJob(...a),
      runGraph: () => Promise.resolve({ job_id: "run-1" }),
      testTrigger: (...a: unknown[]) => testTrigger(...a),
      getNodeRecord: () => Promise.resolve({ Result: { output: {} } }),
      retryRun: () => Promise.resolve({ job_id: "run-2" }),
      cancelRun: () => Promise.resolve({}),
      sampleNode: () => Promise.resolve({}),
      watchFlow: () => new Promise(() => {}),
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

function mount(id = "mention-flow") {
  return render(
    <MemoryRouter initialEntries={[`/flows/${id}`]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
      </Routes>
    </MemoryRouter>,
  );
}

function slackGraph() {
  return {
    id: "mention-flow",
    tenant: "acme",
    workspace: "main",
    name: "Mention relay",
    nodes: [
      { id: "slack_1", module: "slack_on_mention", params: {}, position: { x: 0, y: 0 } },
      { id: "ntfy_1", module: "ntfy", params: { topic: "beans" }, position: { x: 320, y: 0 } },
    ],
    edges: [{ from: "slack_1", from_port: "text", to: "ntfy_1", to_port: "in" }],
  };
}

const fireButton = () => screen.getByText("nodeCard.fireTrigger");

describe("firing a trigger from its card", () => {
  beforeEach(() => {
    loadGraph.mockReset();
    testTrigger.mockReset();
    testTrigger.mockResolvedValue({ job_id: "run-1" });
    localStorage.clear();
  });

  it("offers the button on an event trigger the daemon can seed", async () => {
    loadGraph.mockResolvedValue(slackGraph());
    mount();
    await waitFor(() => expect(fireButton()).toBeTruthy());
  });

  // A schedule produces its own fire moment, so there is no payload to paste
  // and plain Run already exercises it. A button that 400s would be worse.
  it("stays off a schedule trigger", async () => {
    const g = slackGraph();
    loadGraph.mockResolvedValue({
      ...g,
      nodes: [{ ...g.nodes[0], id: "cron_1", module: "cron_trigger" }, g.nodes[1]],
      edges: [{ from: "cron_1", from_port: "fired_at", to: "ntfy_1", to_port: "in" }],
    });
    mount();
    await waitFor(() => expect(screen.getByText("editor.run")).toBeTruthy());
    expect(screen.queryByText("nodeCard.fireTrigger")).toBeNull();
  });

  it("opens the dialog on the provider's own payload shape", async () => {
    loadGraph.mockResolvedValue(slackGraph());
    mount();
    await waitFor(() => expect(fireButton()).toBeTruthy());
    await userEvent.click(fireButton());
    const box = (await screen.findByRole("textbox", { name: "" })) as HTMLTextAreaElement;
    expect(box.value).toContain("app_mention");
    expect(box.value).toContain("team_id");
  });

  it("sends the payload with the id of the step that was clicked", async () => {
    loadGraph.mockResolvedValue(slackGraph());
    mount();
    await waitFor(() => expect(fireButton()).toBeTruthy());
    await userEvent.click(fireButton());
    await userEvent.click(await screen.findByText("editor.testRunFire"));

    await waitFor(() => expect(testTrigger).toHaveBeenCalled());
    const args = testTrigger.mock.calls[0];
    expect(args[5]).toBe("slack_1");
    const sample = args[4] as { event?: { type?: string } };
    expect(sample.event?.type).toBe("app_mention");
  });

  // Per step, not per flow: a Slack envelope and a webhook body have nothing
  // in common, so one must not overwrite the other.
  it("remembers the payload against the step", async () => {
    loadGraph.mockResolvedValue(slackGraph());
    mount();
    await waitFor(() => expect(fireButton()).toBeTruthy());
    await userEvent.click(fireButton());
    await screen.findByRole("textbox", { name: "" });
    await userEvent.click(screen.getByText("common.dismiss"));

    await waitFor(() =>
      expect(localStorage.getItem("dazyflow.testEvent.mention-flow#slack_1")).toContain(
        "app_mention",
      ),
    );
    expect(localStorage.getItem("dazyflow.testEvent.mention-flow")).toBeNull();
  });

  // The toolbar keeps Run on a Slack flow: the steps after the trigger are
  // still worth running, and the swap to "Send test event" is webhook-only.
  it("leaves the toolbar's Run button alone", async () => {
    loadGraph.mockResolvedValue(slackGraph());
    mount();
    await waitFor(() => expect(screen.getByText("editor.run")).toBeTruthy());
    expect(screen.queryByText("editor.testEvent")).toBeNull();
  });
});
