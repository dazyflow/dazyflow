// SPDX-FileCopyrightText: 2026 Angels' Ware
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

// runnerTargetOf names where a configured step will run: the tags a machine has
// to carry, joined the way the rule reads.
//
// Joined with "+" rather than ", " because every tag must match. A comma reads
// as a choice between them, which is the opposite of what the step does — and on
// the canvas this chip is the only place the rule is visible at a glance.
//
// Returns "" when nothing is chosen yet, which is the state a step is in for as
// long as it takes someone to fill the field in, so callers must handle it
// rather than assume a target exists.
export function runnerTargetOf(params: Record<string, unknown> | undefined): string {
  if (!params) return "";
  const tags = Array.isArray(params.tags)
    ? params.tags.filter((t): t is string => typeof t === "string").map((t) => t.trim())
    : [];
  const named = tags.filter((t) => t !== "");
  if (named.length > 0) return named.join(" + ");
  // The pre-tags params, for a flow saved before this step took tags. Both were
  // a single target and both are one tag now — a machine's name is itself a tag
  // — so an old step still reads correctly on the canvas instead of going blank
  // and looking unconfigured. The drop honours them at run time the same way.
  for (const legacy of ["runner", "label"]) {
    const v = typeof params[legacy] === "string" ? (params[legacy] as string).trim() : "";
    if (v) return v;
  }
  return "";
}
