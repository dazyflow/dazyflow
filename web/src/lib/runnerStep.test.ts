// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { isRunnerStep, runnerTargetOf } from "./runnerStep";

// Which machine a step runs on is a PARAMETER, not part of the step's id.
// An earlier design encoded it in the id; these tests exist partly so that
// mistake cannot come back unnoticed.
describe("runnerStep", () => {
  it("recognises the run-on-a-machine step", () => {
    expect(isRunnerStep("run_on_runner")).toBe(true);
    expect(isRunnerStep("http_request")).toBe(false);
    expect(isRunnerStep(undefined)).toBe(false);
    // The old namespaced form is not a step id any more.
    expect(isRunnerStep("runner/invoices/fetch")).toBe(false);
  });

  it("names the tags a step targets", () => {
    expect(runnerTargetOf({ tags: ["invoices-box"] })).toBe("invoices-box");
  });

  it("joins several tags with + because every one must match", () => {
    // "linux, gpu" would read as a choice between them, which is the opposite
    // of what the step does — and this chip is the only place on the canvas
    // where the rule is visible at a glance.
    expect(runnerTargetOf({ tags: ["linux", "gpu"] })).toBe("linux + gpu");
  });

  it("still reads a step saved before this field took tags", () => {
    // Those flows are in production. Both old params were a single target and
    // both are one tag now, so an old step reads correctly instead of going
    // blank and looking unconfigured.
    expect(runnerTargetOf({ runner: "invoices-box" })).toBe("invoices-box");
    expect(runnerTargetOf({ label: "linux" })).toBe("linux");
    // Once tags are set they are the answer; the leftovers are ignored, exactly
    // as the drop ignores them at run time.
    expect(runnerTargetOf({ tags: ["gpu"], runner: "old-box" })).toBe("gpu");
  });

  it("returns nothing for a step nobody has configured yet", () => {
    // The state every step is in for as long as it takes to fill the field in,
    // so callers must handle it rather than assume a target exists.
    expect(runnerTargetOf({})).toBe("");
    expect(runnerTargetOf(undefined)).toBe("");
    expect(runnerTargetOf({ tags: [] })).toBe("");
    expect(runnerTargetOf({ tags: ["  ", ""] })).toBe("");
    expect(runnerTargetOf({ tags: "linux" })).toBe("");
    expect(runnerTargetOf({ tags: [42] })).toBe("");
    expect(runnerTargetOf({ runner: "   " })).toBe("");
    expect(runnerTargetOf({ runner: 42 })).toBe("");
  });
});
