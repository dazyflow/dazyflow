// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Shared rig for the FlowEditor tests.
//
// The editor carries a 26-atom / 27-effect state machine, which is exactly the
// code that cannot be safely refactored into hooks without tests: `tsc` does not
// catch a stale closure or a wrong dependency array. These helpers let each test
// say what it is about rather than restate six mocks.
//
// Two things make the editor testable at all:
//
//   api.streamJob is the ONLY route by which run state reaches the canvas, so
//   capturing `onEvent` lets a test drive the exact frame sequence the daemon
//   would send, synchronously and in orders that are hard to reproduce against a
//   real daemon.
//
//   React Flow reads element geometry through ResizeObserver and
//   getBoundingClientRect, which jsdom does not implement — both must be stubbed
//   with non-zero sizes or the canvas renders nothing and every assertion fails
//   for the wrong reason.

import { vi } from "vitest";


export type StreamFrame = [kind: string, data: unknown];

interface Subscription {
  runID: string;
  emit: (kind: string, data: unknown) => void;
  aborted: () => boolean;
}

// makeStreamJob returns a streamJob mock plus a handle on the subscriptions it
// receives.
//
// The promise it returns settles when the signal aborts, and that detail
// matters: the editor clears its `running` flag in a .finally() on this promise,
// not in the terminal handler. In production the terminal frame makes the editor
// abort its own stream, which settles the underlying fetch. A mock whose promise
// never settles leaves the toolbar stuck on Stop forever — a bug in the mock,
// not the editor, and it cost a confusing round of red tests to work out.
export function makeStreamJob() {
  const subs: Subscription[] = [];
  const streamJob = vi.fn(
    (
      _token: string,
      runID: string,
      onEvent: (kind: string, data: unknown) => void,
      signal: AbortSignal,
    ) => {
      subs.push({ runID, emit: onEvent, aborted: () => signal.aborted });
      return new Promise<void>((resolve) => {
        if (signal.aborted) return resolve();
        signal.addEventListener("abort", () => resolve());
      });
    },
  );
  return {
    streamJob,
    subs,
    latest: () => subs[subs.length - 1],
  };
}

// Frame builders, so a test reads as a sequence of events rather than a pile of
// object literals. Shapes mirror what daemon/runstream emits.
export const frame = {
  node: (nodeID: string, status: string): StreamFrame => [
    "node",
    { node_id: nodeID, status },
  ],
  // A skipped step arrives with the reason it was skipped (core.SkipCode*).
  skipped: (nodeID: string, skipCode: string): StreamFrame => [
    "node",
    { node_id: nodeID, status: "skipped", skip_code: skipCode },
  ],
  progress: (nodeID: string, line: string): StreamFrame => [
    "progress",
    { node_id: nodeID, progress: { data: { stream: "stdout", line } } },
  ],
  paused: (nodeID: string, stepping = false): StreamFrame => [
    "paused",
    { node_id: nodeID, stepping },
  ],
  terminal: (
    status: "succeeded" | "failed" | "cancelled",
    error?: { code?: string; message?: string },
  ): StreamFrame => ["terminal", { status, ...(error ? { error } : {}) }],
};


const RECT = {
  x: 0,
  y: 0,
  top: 0,
  left: 0,
  right: 1200,
  bottom: 800,
  width: 1200,
  height: 800,
  toJSON: () => ({}),
} as DOMRect;

// installLayoutStubs gives jsdom just enough geometry for React Flow to mount
// and render nodes. Without it the canvas measures 0×0 and skips rendering
// entirely, so a missing node in an assertion would mean "no layout", not "the
// editor didn't put it there".
export function installLayoutStubs() {
  if (!globalThis.ResizeObserver) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  }
  if (!globalThis.DOMMatrixReadOnly) {
    globalThis.DOMMatrixReadOnly = class {
      m22 = 1;
      constructor(_t?: string) {}
    } as unknown as typeof DOMMatrixReadOnly;
  }
  Element.prototype.getBoundingClientRect = () => RECT;
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
  if (!globalThis.matchMedia) {
    globalThis.matchMedia = ((q: string) => ({
      matches: false,
      media: q,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
      onchange: null,
    })) as unknown as typeof matchMedia;
  }
}


export function twoStepGraph(id = "coffee-reorder") {
  return {
    id,
    tenant: "acme",
    workspace: "main",
    name: "Coffee reorder",
    nodes: [
      { id: "manual_1", module: "manual_trigger", params: {}, position: { x: 0, y: 0 } },
      { id: "ntfy_1", module: "ntfy", params: { topic: "beans" }, position: { x: 320, y: 0 } },
    ],
    edges: [
      { from: "manual_1", from_port: "out", to: "ntfy_1", to_port: "in" },
    ],
    triggers: [],
  };
}

export const manifests = [
  {
    id: "manual_trigger",
    label: "Run manually",
    category: "trigger",
    inputs: [],
    outputs: [{ port: "out", label: "Out" }],
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

