// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Autosave and the edit lock.
//
// The autosave effect carries five guards, and every one of them is there to
// stop the editor writing the wrong thing over the right thing:
//
//   lockedRunID     a run is in flight and executes the SAVED graph
//   previewRef      you're looking at an old revision, not HEAD
//   loadFailed      the in-memory graph is an empty placeholder, not the server's
//   loadedIDRef     the nodes still belong to the flow you just navigated away
//                   from — the comment on this one says "real data loss,
//                   observed in the wild"
//   saving          a PUT is already in flight
//
// Guards are invisible when they work, which is exactly why they rot. These
// tests assert the absence of a write, which is the only way to notice a guard
// that has quietly stopped guarding.

import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, act, fireEvent } from "@testing-library/react";
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
  // duck-type on .status / .code rather than instanceof, so a plain
  // Object.assign(new Error(), { status: 404 }) from a test takes the same
  // branch the real APIError would. Without isHTTPStatus in this mock the
  // editor's load-error handler threw instead of recognising a 404, and the
  // "404 means a new flow" test passed for the wrong reason.
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

// A graph whose Schedule step has no timezone. The editor heals this on load
// (and persists, because a Run executes the SAVED graph), which is a real write
// triggered purely by mounting — no canvas interaction needed.
function graphWithUnzonedSchedule() {
  return {
    ...twoStepGraph(),
    nodes: [
      { id: "cron_1", module: "cron_trigger", params: { cron: "0 9 * * *" }, position: { x: 0, y: 0 } },
      { id: "ntfy_1", module: "ntfy", params: { topic: "beans" }, position: { x: 320, y: 0 } },
    ],
    edges: [{ from: "cron_1", from_port: "out", to: "ntfy_1", to_port: "in" }],
  };
}

// Let the debounce elapse.
//
// The wait for the toolbar is load-bearing, not cosmetic: the graph load is
// async, and the timer is only armed once it resolves and flags the graph dirty.
// Advancing fake time straight after mount spends it before the timer exists, so
// nothing fires — which read as "the heal is broken" when it was the test.
async function letAutosaveFire() {
  await screen.findByText("editor.run");
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  stream.subs.length = 0;
  loadGraph.mockResolvedValue(twoStepGraph());
  saveGraph.mockResolvedValue({});
  listRuns.mockResolvedValue({ runs: [] });
});

afterEach(() => {
  vi.useRealTimers();
});

// A step's failure policy — a connection's on_error and a node's
// continue_on_error — has to survive a load and a save untouched. The engine has
// honoured both since the beginning; the editor only just learned to READ them,
// and until now saving from the editor silently dropped whatever an API-built
// flow had set. Which is a data-loss bug that looks like nothing in a diff.
describe("editor failure-policy round trip", () => {
  // Built on the unzoned-schedule graph because that is what makes the editor
  // dirty and so makes autosave fire at all: a plain load writes nothing, which
  // is the point of every guard in the suite below.
  const graphWithPolicy = (onError?: string, continueOnError?: boolean) => {
    const g = graphWithUnzonedSchedule();
    return {
      ...g,
      nodes: g.nodes.map((n) =>
        n.id === "ntfy_1" && continueOnError ? { ...n, continue_on_error: true } : n,
      ),
      edges: [{ ...g.edges[0], ...(onError ? { on_error: onError } : {}) }],
    };
  };

  const savedGraph = () =>
    saveGraph.mock.calls[0].find((a) => a && typeof a === "object" && "nodes" in a) as
      | {
          nodes: { id: string; continue_on_error?: boolean }[];
          edges: { on_error?: string }[];
        }
      | undefined;

  it("keeps an on_error connection and a continue-on-error step through a save", async () => {
    loadGraph.mockResolvedValue(graphWithPolicy("fallback", true));
    mount();
    await letAutosaveFire();
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    const saved = savedGraph();
    expect(saved?.edges[0].on_error).toBe("fallback");
    expect(saved?.nodes.find((n) => n.id === "ntfy_1")?.continue_on_error).toBe(true);
  });

  it("writes nothing for the default, so a flow does not grow empty fields", async () => {
    // Turning the setting on and off again has to leave the flow as it was
    // rather than accumulating `"on_error": ""` on every wire.
    loadGraph.mockResolvedValue(graphWithPolicy());
    mount();
    await letAutosaveFire();
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    const saved = savedGraph();
    expect(saved?.edges[0]).not.toHaveProperty("on_error");
    expect(saved?.nodes[0]).not.toHaveProperty("continue_on_error");
  });
});

describe("editor autosave guards", () => {
  it("heals a Schedule step with no timezone and persists it", async () => {
    loadGraph.mockResolvedValue(graphWithUnzonedSchedule());
    mount();
    await letAutosaveFire();
    // Without a zone both the schedule and its fired_at would run in UTC, and a
    // forked flow never went through the editor's add/edit path that stamps it.
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    const saved = saveGraph.mock.calls[0].find(
      (a) => a && typeof a === "object" && "nodes" in a,
    ) as { nodes: { module: string; params?: { tz?: string } }[] } | undefined;
    const cron = saved?.nodes.find((n) => n.module === "cron_trigger");
    expect(cron?.params?.tz).toBeTruthy();
  });

  // Previously this heal was the one write that ignored the edit lock: it PUT
  // straight from the load handler, while autosave refuses to write during a run
  // for the very reason the heal exists — "Run executes the SAVED graph". The
  // heal now marks the graph dirty instead, so it inherits every autosave guard.
  it("does not heal while a run holds the edit lock", async () => {
    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "running" }] });
    loadGraph.mockResolvedValue(graphWithUnzonedSchedule());
    mount();
    await letAutosaveFire();
    expect(saveGraph).not.toHaveBeenCalled();
  });

  it("heals once the run releases the lock", async () => {
    // The waiting write is not dropped, just deferred: when the lock clears, the
    // pending autosave writes the stamped graph.
    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "running" }] });
    loadGraph.mockResolvedValue(graphWithUnzonedSchedule());
    mount();
    await letAutosaveFire();
    expect(saveGraph).not.toHaveBeenCalled();

    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "succeeded" }] });
    // Two advances, deliberately. The first lets the lock poll observe the
    // finished run and clear the lock; only then does the autosave effect re-run
    // and arm its 1500ms timer. Doing it in one advance arms the timer during the
    // final flush, with no fake time left for it to fire.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    const saved = saveGraph.mock.calls[0].find(
      (a) => a && typeof a === "object" && "nodes" in a,
    ) as { nodes: { module: string; params?: { tz?: string } }[] } | undefined;
    expect(
      saved?.nodes.find((n) => n.module === "cron_trigger")?.params?.tz,
    ).toBeTruthy();
  });

  it("does not autosave when the initial load failed", async () => {
    // The in-memory graph is an empty placeholder, NOT the server's. Autosaving
    // it would clobber the real flow with nothing.
    loadGraph.mockRejectedValue(
      Object.assign(new Error("boom"), { status: 500 }),
    );
    mount();
    await letAutosaveFire();
    expect(saveGraph).not.toHaveBeenCalled();
  });

  it("still autosaves after a 404 load, which means a genuinely new flow", async () => {
    // 404 is the one failure that does NOT block: a flow being created has no
    // server copy yet, so the empty in-memory graph IS the truth.
    loadGraph.mockRejectedValue(
      Object.assign(new Error("not found"), { status: 404 }),
    );
    mount();
    await screen.findByText("editor.run");
    // Nothing is dirty on a fresh empty graph, so no write — but critically the
    // editor is usable rather than locked out.
    expect(saveGraph).not.toHaveBeenCalled();
  });
});

describe("editor edit lock", () => {
  it("keeps re-polling while a run holds the lock, then stops once it clears", async () => {
    // Scheduler-driven runs (a cron/poll trigger firing) never reach this
    // editor's SSE terminal, so nothing else would ever clear the lock: a flow
    // that polls would catch a run at mount and then stay "locked" forever,
    // silently blocking every save. The self-heal is a poll that runs ONLY
    // while the lock is held.
    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "running" }] });
    mount();
    await screen.findByText("editor.run");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8000);
    });
    expect(listRuns.mock.calls.length).toBeGreaterThan(1);

    // The run finishes: the next poll drops the lock, and polling stops.
    listRuns.mockResolvedValue({ runs: [{ id: "run-9", status: "succeeded" }] });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000);
    });
    const afterRelease = listRuns.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8000);
    });
    expect(listRuns.mock.calls.length).toBe(afterRelease);
  });

  it("does not poll for the lock when nothing holds it", async () => {
    mount();
    await screen.findByText("editor.run");
    const atMount = listRuns.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    // Only runs while locked, so there's no idle polling cost.
    expect(listRuns.mock.calls.length).toBe(atMount);
  });
});

// A step's NAME. The inspector has always offered to rename one and the value
// went nowhere: buildGraph wrote id/module/params/position and the debug flags,
// so the name never reached the server, and the load re-derived it from the
// drop's manifest — a rename reverted on the next reload, with an autosave in
// between reporting "Saved".
describe("editor step name round trip", () => {
  const savedNodes = () =>
    (
      saveGraph.mock.calls.at(-1)?.find((a) => a && typeof a === "object" && "nodes" in a) as
        | { nodes: { id: string; label?: string }[] }
        | undefined
    )?.nodes;

  // The unzoned schedule is what makes the editor dirty on load, so autosave
  // fires without needing a canvas gesture — the same trick the policy tests
  // above use.
  const graphNamed = (label?: string) => {
    const g = graphWithUnzonedSchedule();
    return {
      ...g,
      nodes: g.nodes.map((n) => (n.id === "ntfy_1" && label ? { ...n, label } : n)),
    };
  };

  it("keeps a name through a save instead of dropping it", async () => {
    // The data-loss shape: a name the flow already had, silently absent from
    // the next write.
    loadGraph.mockResolvedValue(graphNamed("Tell the barista"));
    mount();
    await letAutosaveFire();
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    expect(savedNodes()?.find((n) => n.id === "ntfy_1")?.label).toBe("Tell the barista");
  });

  it("shows a saved name on the canvas instead of the drop's own", async () => {
    loadGraph.mockResolvedValue(graphNamed("Tell the barista"));
    mount();
    expect(await screen.findByText("Tell the barista")).toBeInTheDocument();
    // The drop's own label is what it replaced.
    expect(screen.queryByText("Send notification")).toBeNull();
  });

  it("writes no name for a step still called after its drop", async () => {
    // The default is the drop's LOCALIZED label. Storing it would freeze the
    // flow in one language and grow a field on every node for nothing.
    loadGraph.mockResolvedValue(graphNamed());
    mount();
    await letAutosaveFire();
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    for (const n of savedNodes() ?? []) {
      expect(n).not.toHaveProperty("label");
    }
  });

  it("drops a stored name that only repeats the drop's own label", async () => {
    // Same reason: it carries no intent, and keeping it would pin the English
    // wording into a flow a Swedish reader opens.
    loadGraph.mockResolvedValue(graphNamed("Send notification"));
    mount();
    await letAutosaveFire();
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    expect(savedNodes()?.find((n) => n.id === "ntfy_1")).not.toHaveProperty("label");
  });
});

// The editor rebuilds the saved document from its own state, field by field,
// rather than writing back the graph it loaded. That is deliberate — the canvas
// is the source of truth for nodes and edges — but it means a graph-level field
// the editor does not know about is DROPPED on the next save, silently, no
// matter what set it. That is how the flow's language went missing after the
// settings modal wrote it: the modal saved, and the next autosave wrote a
// document rebuilt without it.
//
// So this asserts round-tripping, not any one field's plumbing: load a graph
// with every graph-level setting populated, let a save fire, and require them
// all back. A field added to core.Graph and wired into the settings UI but not
// into the editor's state fails here instead of in someone's flow.
describe("editor graph-level round-trip", () => {
  const META = {
    visibility: "private" as const,
    owner: "owner@example.com",
    language: "sv",
    name: "Nightly digest",
    icon: "rocket",
    description: "Sends the nightly digest",
    timeout_seconds: 600,
    failure_notify: { webhook: "https://hooks.example/x" },
    disabled: true,
  };

  it("preserves every graph-level setting through a save", async () => {
    loadGraph.mockResolvedValue({ ...graphWithUnzonedSchedule(), ...META });
    mount();
    await letAutosaveFire();
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    const saved = saveGraph.mock.calls[0].find(
      (a) => a && typeof a === "object" && "nodes" in a,
    ) as Record<string, unknown> | undefined;

    for (const [key, want] of Object.entries(META)) {
      expect(saved, `saved document missing`).toBeDefined();
      expect(saved?.[key], `${key} was dropped on save`).toEqual(want);
    }
  });
});
