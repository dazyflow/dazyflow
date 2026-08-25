// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The step that runs a script on one of the org's own machines.
//
// There is exactly one such step, `run_on_runner`, and WHICH machine it targets
// is a parameter rather than part of its id. That is a deliberate consequence of
// how runners work now: a runner does not contribute steps to the catalog, it
// executes a command a flow supplies. So the id identifies the capability and
// the params identify the destination.
//
// (An earlier design gave every runner its own namespaced step ids,
// `runner/<runner>/<step>`. This file used to parse those. Nothing does now.)
export const RUNNER_STEP = "run_on_runner";

// isRunnerStep reports whether a module id is the run-on-a-machine step.
export function isRunnerStep(moduleID: string | undefined): boolean {
  return moduleID === RUNNER_STEP;
}

// runnerTargetOf names where a configured step will run — a machine, or a
// label standing for whichever machine is free.
//
// Returns "" when nothing is chosen yet, which is the state a step is in for as
// long as it takes someone to fill the field in, so callers must handle it
// rather than assume a target exists.
export function runnerTargetOf(params: Record<string, unknown> | undefined): string {
  if (!params) return "";
  const named = typeof params.runner === "string" ? params.runner.trim() : "";
  if (named) return named;
  const label = typeof params.label === "string" ? params.label.trim() : "";
  return label;
}
