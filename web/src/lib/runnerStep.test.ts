// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

  it("names the machine a step targets", () => {
    expect(runnerTargetOf({ runner: "invoices-box" })).toBe("invoices-box");
  });

  it("falls back to the label when no machine is named", () => {
    expect(runnerTargetOf({ label: "linux" })).toBe("linux");
  });

  it("prefers a named machine over a label", () => {
    // The step refuses both at run time, but the editor still has to render
    // something sensible while someone is mid-edit.
    expect(runnerTargetOf({ runner: "box", label: "linux" })).toBe("box");
  });

  it("returns nothing for a step nobody has configured yet", () => {
    // The state every step is in for as long as it takes to fill the field in,
    // so callers must handle it rather than assume a target exists.
    expect(runnerTargetOf({})).toBe("");
    expect(runnerTargetOf(undefined)).toBe("");
    expect(runnerTargetOf({ runner: "   " })).toBe("");
    expect(runnerTargetOf({ runner: 42 })).toBe("");
  });
});
