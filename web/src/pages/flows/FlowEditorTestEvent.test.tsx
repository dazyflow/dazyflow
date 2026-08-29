// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// "Send test event" — the control that replaces Run on a webhook flow.
//
// Clicking Run on a webhook flow hands its webhook_input node no body, so the
// editor offers a test event instead: a synthetic payload fed through the same
// seed-building path a real /trigger hit uses.
//
// It was unreachable. The condition also required a graph-level webhook entry
// in g.triggers, and trigger config moved onto the nodes when the Triggers
// menu went away — so every webhook flow built since had `triggers: null`, the
// condition was never true, and the button never rendered. Nothing failed: the
// endpoint worked, the affordance existed, and the only symptom was a user
// looking for a button that was never drawn.
//
// That is why this test asserts on a graph with `triggers` ABSENT. A fixture
// carrying a legacy trigger array would have passed against the broken code.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { installLayoutStubs, makeStreamJob, twoStepGraph } from "./editorTestHarness";

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

// webhook_input has to be a known module or the canvas cannot render the node
// the whole assertion depends on.
const webhookManifests = [
  {
    id: "webhook_input",
    label: "Webhook",
    category: "trigger",
    inputs: [],
    outputs: [{ port: "body", label: "Body" }],
    params_schema: {
      type: "object",
      properties: { secrets: { type: "array", title: "Keys" } },
    },
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
  return {
    APIError,
    isHTTPStatus: () => false,
    isErrorCode: () => false,
    api: {
      loadGraph: (...a: unknown[]) => loadGraph(...a),
      saveGraph: () => Promise.resolve({ commit: "c1" }),
      listRuns: () => Promise.resolve({ runs: [] }),
      listDrops: () => Promise.resolve({ drops: webhookManifests }),
      dropSuggestions: () => Promise.resolve([]),
      listSecrets: () => Promise.resolve({ secrets: [] }),
      listProviders: () => Promise.resolve({ providers: [] }),
      getPublishedInfo: () => Promise.resolve({ published: true }),
      flowHistory: () => Promise.resolve({ revisions: [] }),
      streamJob: (...a: never[]) => stream.streamJob(...a),
      runGraph: () => Promise.resolve({ job_id: "run-1" }),
      testTriggerFlow: () => Promise.resolve({ job_id: "run-1" }),
      getNodeRecord: () => Promise.resolve({ Result: { output: {} } }),
      retryRun: () => Promise.resolve({ job_id: "run-2" }),
      cancelRun: () => Promise.resolve({}),
      sampleNode: () => Promise.resolve({}),
      watchFlow: () => new Promise(() => {}), // stays open; not under test here
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

// A webhook flow in the shape the product actually produces now: the secret
// lives on the node, and there is NO graph-level triggers array.
function webhookGraph() {
  return {
    id: "coffee-reorder",
    tenant: "acme",
    workspace: "main",
    name: "Release",
    nodes: [
      {
        id: "webhook_input_1",
        module: "webhook_input",
        params: { secrets: ["s3cr3t"] },
        position: { x: 0, y: 0 },
      },
      { id: "ntfy_1", module: "ntfy", params: { topic: "beans" }, position: { x: 320, y: 0 } },
    ],
    edges: [{ from: "webhook_input_1", from_port: "body", to: "ntfy_1", to_port: "in" }],
  };
}

describe("the test-event control", () => {
  beforeEach(() => {
    loadGraph.mockReset();
  });

  it("appears for a webhook flow whose trigger lives on the node", async () => {
    loadGraph.mockResolvedValue(webhookGraph());
    mount();
    // The regression: this graph has no `triggers` array at all, which is what
    // every webhook flow built since the Triggers menu was removed looks like.
    await waitFor(() =>
      expect(screen.getByText("editor.testEvent")).toBeTruthy(),
    );
  });

  it("still appears when a legacy graph-level trigger is present", async () => {
    loadGraph.mockResolvedValue({
      ...webhookGraph(),
      triggers: [{ type: "webhook" }],
    });
    mount();
    await waitFor(() =>
      expect(screen.getByText("editor.testEvent")).toBeTruthy(),
    );
  });

  it("is not offered on a flow with no webhook node", async () => {
    loadGraph.mockResolvedValue(twoStepGraph());
    mount();
    // Wait for the editor to settle on its normal Run control, then assert the
    // test-event button is absent — otherwise this passes while still loading.
    await waitFor(() => expect(screen.getByText("editor.run")).toBeTruthy());
    expect(screen.queryByText("editor.testEvent")).toBeNull();
  });
});
