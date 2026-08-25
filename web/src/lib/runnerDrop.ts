// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Steps contributed by one of the org's own runners.
//
// A runner step is identified by its id, not by a flag on the manifest: the id
// is `runner/<runner>/<step>`, the prefix is reserved server-side (the native
// registry refuses it), and it is the one piece of information a saved graph
// always carries. Reading it off the id means a graph loaded from history, an
// export, or a fork all agree on which steps leave the daemon — without
// depending on a catalog lookup that may not have happened yet.
const PREFIX = "runner/";

// isRunnerStep reports whether a module id belongs to a tenant runner.
export function isRunnerStep(moduleID: string | undefined): boolean {
  return !!moduleID && moduleID.startsWith(PREFIX);
}

// runnerNameOf pulls the runner's name out of a step id, for the "runs on
// your hardware · <name>" line. Returns "" for anything that isn't a runner
// step, so callers can use it without checking first.
export function runnerNameOf(moduleID: string | undefined): string {
  if (!isRunnerStep(moduleID)) return "";
  return moduleID!.slice(PREFIX.length).split("/")[0] ?? "";
}
