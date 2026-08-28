// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Edge, Ref } from "../types";

// What a person means by "the result" of a run is the output of the steps at
// the end of the flow — not the intermediate plumbing. Both the editor (after
// pressing Run) and the run-detail page answer that question, and they have
// to answer it the same way, so the picking and the formatting live here.

// RUN_PREVIEW_MAX caps an inline result so a step that emitted a thousand
// rows still leaves the banner a banner. Full values stay available in the
// run timeline's per-port disclosure.
export const RUN_PREVIEW_MAX = 600;

// previewOutput renders a step's output ports as one short human-readable
// blob: the first port carrying an inline value wins. Text passes through
// as-is (a rendered summary, a message body); anything structured is
// pretty-printed. Returns "" when the step produced nothing inline — which
// includes outputs held by reference (a large blob in storage), where the
// internal ref string would mean nothing to the reader.
export function previewOutput(
  output: Record<string, Ref> | undefined,
  max: number = RUN_PREVIEW_MAX,
): string {
  for (const port of Object.keys(output ?? {})) {
    const data = output?.[port]?.data;
    if (data == null) continue;
    let text: string;
    if (typeof data === "string") {
      text = data;
    } else {
      try {
        text = JSON.stringify(data, null, 2);
      } catch {
        text = String(data);
      }
    }
    if (text.trim() === "") continue;
    return text.length > max ? text.slice(0, max) + "…" : text;
  }
  return "";
}

// isResultNode reports whether a node sits at the end of the flow — no edge
// leaves it. Callers walk their own node list in execution order and take the
// first result node that produced something, so a fan-out with several
// endpoints still shows a real value rather than nothing.
export function isResultNode(nodeID: string, edges: Edge[] | undefined): boolean {
  return !(edges ?? []).some((e) => e.from === nodeID);
}
