// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { isRunnerStep, runnerNameOf } from "./runnerDrop";

// Identification is by id, not by a manifest flag, so a graph loaded from
// history or an export agrees with a freshly resolved one about which steps
// leave the daemon.
describe("runnerDrop", () => {
  it("recognises a runner step by its reserved prefix", () => {
    expect(isRunnerStep("runner/invoices/fetch")).toBe(true);
    expect(isRunnerStep("http_request")).toBe(false);
    // Only a prefix is reserved — an ordinary drop may contain the word.
    expect(isRunnerStep("start_runner")).toBe(false);
    expect(isRunnerStep(undefined)).toBe(false);
    expect(isRunnerStep("")).toBe(false);
  });

  it("names the runner a step belongs to", () => {
    expect(runnerNameOf("runner/invoices/fetch")).toBe("invoices");
    // A step name containing a slash still resolves to the runner.
    expect(runnerNameOf("runner/invoices/nested/thing")).toBe("invoices");
  });

  it("returns nothing for a step that is not a runner's", () => {
    // Callers use this without checking isRunnerStep first, so it must be
    // safe rather than throwing or returning a misleading fragment.
    expect(runnerNameOf("http_request")).toBe("");
    expect(runnerNameOf(undefined)).toBe("");
  });
});
