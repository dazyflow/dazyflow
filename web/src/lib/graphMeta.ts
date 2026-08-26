// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The flow's SETTINGS — everything on a Graph that isn't its structure.
//
// This list exists because the editor had two of them, and they disagreed.
// FlowEditor rebuilds the document it saves from its own React state (the
// canvas is the truth for steps and wires), and the settings modal, which
// edits graph-level fields, passes them as overrides — because a setState a
// few lines earlier has not applied yet, so reading state would save the
// PREVIOUS value. Both lists were written by hand, and both were incomplete:
// the flow's language went missing from each, and failure_notify from one, so
// setting a failure webhook worked until the next canvas edit and setting a
// language never worked at all.
//
// One list, checked by the compiler. Add a field to Graph and TypeScript names
// it below until you decide which side it belongs on.
import type { Graph } from "../types";

// NotSettings are the keys this module deliberately does not carry, each for a
// reason:
//
//	id / tenant / workspace / version   the flow's identity, not a setting
//	nodes / edges / frames              the canvas, rebuilt from editor state
//	triggers                            graph-level triggers need their own
//	                                    empty→undefined handling at the call
//	                                    site, so they stay explicit
//	owner                               set by the daemon on first save and
//	                                    never editable here
//	disabled                            the pause switch, which the editor
//	                                    holds as its own state and writes only
//	                                    when true (omitempty drops false)
type NotSettings =
  | "id"
  | "version"
  | "tenant"
  | "workspace"
  | "nodes"
  | "edges"
  | "frames"
  | "triggers"
  | "owner"
  | "disabled";

export type GraphSettingKey = Exclude<keyof Graph, NotSettings>;

// GRAPH_SETTING_KEYS is every graph-level setting the editor round-trips.
export const GRAPH_SETTING_KEYS = [
  "visibility",
  "language",
  "name",
  "icon",
  "description",
  "timeout_seconds",
  "failure_notify",
] as const satisfies readonly GraphSettingKey[];

// The guard, and the reason this file is worth its length: adding a field to
// Graph without listing it above (or excluding it in NotSettings, with a
// reason) fails to compile, and the error names the field. A runtime test
// cannot do this — TypeScript types don't exist at runtime — and the failure
// it prevents is silent: a setting that saves, reverts, and reports nothing.
type UnlistedSettingKey = Exclude<GraphSettingKey, (typeof GRAPH_SETTING_KEYS)[number]>;
const _everySettingListed: UnlistedSettingKey extends never
  ? true
  : ["add these to GRAPH_SETTING_KEYS, or to NotSettings with a reason", UnlistedSettingKey] = true;
void _everySettingListed;

// pickGraphSettings copies the settings present on `g`, leaving absent ones
// absent — writing `undefined` explicitly would make a flow grow empty fields
// on every save.
export function pickGraphSettings(g: Partial<Graph>): Partial<Graph> {
  const out: Partial<Graph> = {};
  for (const key of GRAPH_SETTING_KEYS) {
    if (g[key] !== undefined) {
      // Each key's value type is its own; the loop erases that, and a mapped
      // assignment is the one place a cast is honest about what it is.
      (out as Record<string, unknown>)[key] = g[key];
    }
  }
  return out;
}
