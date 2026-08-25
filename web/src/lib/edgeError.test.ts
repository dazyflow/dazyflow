// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The engine has honoured Edge.OnError since the beginning and the editor could
// not set it, so a flow author had to reach for the API or the flow's JSON to
// say "run this only if that failed". These pin the editor's half: that the
// four modes are the four the engine implements, that an unknown one reads as
// the safe default, and that each is drawn differently — error handling you
// cannot see on the canvas is error handling nobody will trust.
import { describe, expect, it } from "vitest";
import {
  asEdgeErrorMode,
  edgeErrorLabelKey,
  edgeErrorStyle,
  retryAvailable,
  ROUTING_MODES,
  type EdgeErrorMode,
} from "./edgeError";

const ALL: EdgeErrorMode[] = ["", "skip", "fallback", "retry"];

describe("asEdgeErrorMode", () => {
  it("keeps the modes the engine implements", () => {
    expect(ALL.map(asEdgeErrorMode)).toEqual(ALL);
  });

  it("reads anything else as the default rather than preserving it", () => {
    // The engine treats an unrecognised value as abort, so showing it as
    // anything else would be a lie about what will happen at run time.
    for (const junk of ["ignore", "ABORT", undefined, null, 3, {}, []]) {
      expect(asEdgeErrorMode(junk)).toBe("");
    }
  });
});

describe("ROUTING_MODES", () => {
  it("is the three answers to one question, without retry", () => {
    // Retry is not routing: for routing it behaves like the default
    // (classifyEdge falls through to blocking) and separately asks the worker
    // to re-run the step. Putting it in the same radio group would claim it
    // excludes the others.
    expect(ROUTING_MODES).toEqual(["", "skip", "fallback"]);
  });
});

describe("edgeErrorStyle", () => {
  it("leaves the default exactly as it was drawn before", () => {
    // So opening an existing flow looks identical, and colour reads as
    // "something was chosen here" rather than as decoration.
    expect(edgeErrorStyle("")).toEqual({ stroke: "var(--accent)", strokeWidth: 1.5 });
  });

  it("gives every non-default mode its own colour AND dash", () => {
    // Two encodings, so the modes stay distinguishable to a reader who cannot
    // tell the two colours apart.
    const seen = new Set<string>();
    for (const mode of ALL.filter((m) => m !== "")) {
      const s = edgeErrorStyle(mode);
      expect(s.strokeDasharray, mode).toBeTruthy();
      const fingerprint = `${s.stroke}|${s.strokeDasharray}`;
      expect(seen.has(fingerprint), `${mode} is drawn the same as another mode`).toBe(false);
      seen.add(fingerprint);
    }
  });

  it("draws the error handler in the danger colour", () => {
    // The one wire that is idle on every run that goes well.
    expect(edgeErrorStyle("fallback").stroke).toBe("var(--danger)");
  });

  it("keeps retry in the default colour, because it routes like the default", () => {
    expect(edgeErrorStyle("retry").stroke).toBe(edgeErrorStyle("").stroke);
    expect(edgeErrorStyle("retry").strokeDasharray).toBeTruthy();
  });
});

describe("edgeErrorLabelKey", () => {
  it("gives each mode its own key", () => {
    const keys = ALL.map(edgeErrorLabelKey);
    expect(new Set(keys).size).toBe(ALL.length);
  });
});

describe("retryAvailable", () => {
  it("is true only for a drop that declares the backoff policy", () => {
    // The worker refuses to retry a module with no policy, so offering the
    // setting elsewhere would be offering one that silently does nothing —
    // the runner step being the deliberate example.
    expect(retryAvailable("exponential_backoff")).toBe(true);
    expect(retryAvailable("never")).toBe(false);
    expect(retryAvailable(undefined)).toBe(false);
    expect(retryAvailable("")).toBe(false);
  });
});
