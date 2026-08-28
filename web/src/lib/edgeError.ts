// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// What a connection does when the step it comes from fails.
//
// The engine has honoured this since the beginning (core.Edge.OnError, applied
// by classifyEdge in daemon/dispatch.go) and nothing in the editor could set
// it: a flow author had to reach for the API, the MCP tools or the flow's JSON
// to say "run this only if that failed". This module is the editor's half —
// the vocabulary, and how each mode is drawn.
//
// Two of the four are about ROUTING and one is not, which is the thing worth
// getting straight before reading the rest:
//
//   ""         the default. The next step runs only if this one succeeded; a
//              failure blocks the branch and fails the run.
//   "skip"     the next step runs either way.
//   "fallback" the next step runs ONLY on failure — dormant on success. This is
//              the error handler.
//   "retry"    not routing at all: it asks the WORKER to run the failed step
//              again with backoff. For routing it behaves like the default
//              (classifyEdge falls through to blocking), so it stacks with
//              neither of the two above.
export type EdgeErrorMode = "" | "skip" | "fallback" | "retry";

// ROUTING_MODES are the three mutually exclusive answers to "when does the next
// step run?". Retry is deliberately absent — see above.
export const ROUTING_MODES: EdgeErrorMode[] = ["", "skip", "fallback"];

// asEdgeErrorMode narrows whatever a stored graph carries. An unrecognised
// value reads as the default rather than being preserved: the engine treats
// anything it does not know as abort (core.OnError.Valid rejects it at the API
// edge anyway), so showing it as anything else would be a lie about what will
// happen.
export function asEdgeErrorMode(v: unknown): EdgeErrorMode {
  switch (v) {
    case "skip":
    case "fallback":
    case "retry":
      return v;
    default:
      return "";
  }
}

// i18nKey names the menu label for a mode. Kept here so the four labels cannot
// drift from the four modes.
export function edgeErrorLabelKey(mode: EdgeErrorMode): string {
  switch (mode) {
    case "skip":
      return "editor.edgeError.skip";
    case "fallback":
      return "editor.edgeError.fallback";
    case "retry":
      return "editor.edgeError.retry";
    default:
      return "editor.edgeError.abort";
  }
}

// EdgeStyle is the subset of React Flow's edge style this module decides.
export type EdgeStyle = {
  stroke: string;
  strokeWidth: number;
  strokeDasharray?: string;
};

// edgeErrorStyle draws the mode, because error handling you cannot see on the
// canvas is not error handling anyone will trust.
//
// The default is left exactly as it was — solid accent — so opening an existing
// flow looks identical and the colouring reads as "something was chosen here"
// rather than as decoration. The other three each get a dash pattern as well as
// a colour, so they stay distinguishable to a reader who cannot tell the two
// colours apart.
export function edgeErrorStyle(mode: EdgeErrorMode): EdgeStyle {
  switch (mode) {
    case "fallback":
      // The error handler. Danger-coloured and finely dotted: this wire is
      // idle on every run that goes well.
      return { stroke: "var(--danger)", strokeWidth: 1.5, strokeDasharray: "2 4" };
    case "skip":
      // Carries on either way — amber for "the failure was tolerated", and a
      // longer dash so it is not mistaken for the handler.
      return { stroke: "var(--status-warning)", strokeWidth: 1.5, strokeDasharray: "7 4" };
    case "retry":
      // Routes like the default, so it keeps the default colour; the dash says
      // something extra was asked for.
      return { stroke: "var(--accent)", strokeWidth: 1.5, strokeDasharray: "1 3" };
    default:
      return { stroke: "var(--accent)", strokeWidth: 1.5 };
  }
}

// retryAvailable reports whether an on_error=retry edge would do anything from
// this step.
//
// The worker refuses to retry a module whose manifest declares no retry policy
// (daemon/worker.go), so offering the setting on those steps would be offering
// one that silently does nothing — the runner step is exactly that case, on
// purpose: nobody can know whether a script already sent the invoices.
export function retryAvailable(retryPolicy: string | undefined): boolean {
  return retryPolicy === "exponential_backoff";
}
