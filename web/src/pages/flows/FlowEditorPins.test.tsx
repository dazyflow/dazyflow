// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pin bounds after a late /drops answer.
//
// The editor asks for the flow and the drop list in parallel, so a card can
// mount before its manifest exists — with placeholder in/out pins instead of
// the ports its wires were drawn to. React Flow re-reads a node's pin
// positions only when the node's box changes size, which an unfolded card
// does when the real ports arrive but a folded one does not: same icon, same
// name, same button. It therefore keeps the placeholder bounds, no edge can
// resolve its handle ids, and every wire to that drop renders as nothing.
// The editor has to say so explicitly, which is what these tests assert —
// jsdom measures no geometry, so the call is the only honest evidence here.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { installLayoutStubs, makeStreamJob } from "./editorTestHarness";

installLayoutStubs();

const repin = vi.fn();
vi.mock("@xyflow/react", async (importActual) => {
  const actual = await importActual<typeof import("@xyflow/react")>();
  return { ...actual, useUpdateNodeInternals: () => repin };
});

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
const listDrops = vi.fn();

vi.mock("../../api", () => {
  class APIError extends Error {
    status: number;
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
      listDrops: (...a: unknown[]) => listDrops(...a),
      saveGraph: () => Promise.resolve({}),
      listRuns: () => Promise.resolve({ runs: [] }),
      dropSuggestions: () => Promise.resolve([]),
      listSecrets: () => Promise.resolve({ secrets: [] }),
      listProviders: () => Promise.resolve({ providers: [] }),
      getPublishedInfo: () => Promise.resolve({ published: false }),
      flowHistory: () => Promise.resolve({ revisions: [] }),
      streamJob: (...a: Parameters<typeof stream.streamJob>) => stream.streamJob(...a),
      watchFlow: () => Promise.resolve({}),
    },
  };
});

import { FlowEditor } from "./FlowEditor";

// Named ports throughout: a drop whose ports happen to be called in/out would
// match the placeholders by luck and prove nothing.
const manifests = [
  {
    id: "text",
    label: "Text",
    category: "input",
    inputs: [],
    outputs: [{ port: "text", label: "Text" }],
    params_schema: { type: "object", properties: {} },
  },
  {
    id: "twilio_send_sms",
    label: "Send SMS",
    category: "notify",
    inputs: [
      { port: "to", label: "To" },
      { port: "body", label: "Body" },
    ],
    outputs: [{ port: "message_sid", label: "Message ID" }],
    params_schema: { type: "object", properties: {} },
  },
];

// Both drops are folded, and both carry a name of the user's own — so neither
// chip changes size when the manifest lands and nothing else would prompt a
// re-measure.
const graph = {
  id: "shop-sms",
  tenant: "acme",
  workspace: "main",
  name: "Shop SMS",
  nodes: [
    {
      id: "text_1",
      module: "text",
      label: "Message",
      params: {},
      position: { x: 0, y: 0 },
      collapsed: true,
    },
    {
      id: "sms_1",
      module: "twilio_send_sms",
      label: "Text the shop",
      params: {},
      position: { x: 320, y: 0 },
      collapsed: true,
    },
  ],
  edges: [{ from: "text_1", from_port: "text", to: "sms_1", to_port: "body" }],
  triggers: [],
};

function mount() {
  return render(
    <MemoryRouter initialEntries={["/flows/shop-sms"]}>
      <Routes>
        <Route path="/flows/:id" element={<FlowEditor />} />
      </Routes>
    </MemoryRouter>,
  );
}

const ready = () => screen.findByText("editor.run");

beforeEach(() => {
  repin.mockClear();
  loadGraph.mockReset();
  listDrops.mockReset();
});

describe("folded cards and a late drop list", () => {
  it("re-reads the pins of every card that was drawn before its manifest", async () => {
    let answerDrops: (() => void) | undefined;
    listDrops.mockReturnValue(
      new Promise((resolve) => {
        answerDrops = () => resolve({ drops: manifests });
      }),
    );
    loadGraph.mockResolvedValue(graph);

    mount();
    await ready();
    await waitFor(() => expect(screen.getByText("Text the shop")).toBeInTheDocument());
    expect(repin).not.toHaveBeenCalled();

    answerDrops?.();
    await waitFor(() => expect(repin).toHaveBeenCalled());
    expect(repin.mock.calls.at(-1)?.[0]).toEqual(
      expect.arrayContaining(["text_1", "sms_1"]),
    );
  });

  it("leaves the pins alone when the drop list arrived first", async () => {
    listDrops.mockResolvedValue({ drops: manifests });
    let answerGraph: (() => void) | undefined;
    loadGraph.mockReturnValue(
      new Promise((resolve) => {
        answerGraph = () => resolve(graph);
      }),
    );

    mount();
    await waitFor(() => expect(listDrops).toHaveBeenCalled());
    answerGraph?.();
    await ready();
    await waitFor(() => expect(screen.getByText("Text the shop")).toBeInTheDocument());

    expect(repin).not.toHaveBeenCalled();
  });
});
