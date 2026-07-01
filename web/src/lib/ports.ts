// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Port } from "../types";

// Port-matching helpers for auto-wiring when a drag-create spawns a new
// node. Pure functions, extracted from FlowEditor so they can be unit
// tested.

// The passthrough pin ("pass") is prepended to every processing drop's
// inputs and outputs by core.WithPassthrough. It's untyped, so it matches
// any MIME — which is exactly why it must not be auto-chosen by default.
export const PASS_PORT = "pass";

// PortKind mirrors core.PortKind (core/manifest.go): the small set of
// human-meaningful value kinds a flow moves between steps, DERIVED from a
// port's MIME. The UI reads this (not raw MIME) so a non-techie sees "Items" or
// "Text", not "application/json".
export type PortKind = "item" | "text" | "bool" | "file" | "any";

export function portKind(p: Pick<Port, "mime">): PortKind {
  const m = p.mime;
  if (!m?.length) return "any";
  if (m.includes("application/json") || m.includes("application/x-dazyflow-list+json")) return "item";
  if (m.includes("text/plain") || m.includes("text/html")) return "text";
  if (m.includes("application/x-bool")) return "bool";
  return "file";
}

// portCardinality is "one" or "many" — a list port carries many. A "table" is
// just many Items; there is no separate rows type.
export function portCardinality(p: Pick<Port, "list">): "one" | "many" {
  return p.list ? "many" : "one";
}

// portTypeLabel is the plain-language description shown to the user for what a
// port carries — kind × cardinality. e.g. "Items" (many records), "Text",
// "Files". Used in the port tooltip so it's obvious what flows down a wire.
export function portTypeLabel(p: Pick<Port, "mime" | "list">): string {
  const many = portCardinality(p) === "many";
  switch (portKind(p)) {
    case "item":
      return many ? "Items (a table)" : "Item";
    case "text":
      return many ? "Texts" : "Text";
    case "bool":
      return "Yes / no";
    case "file":
      return many ? "Files" : "File";
    default:
      return "Anything";
  }
}

// mimeCompatible reports whether two MIME sets could carry the same value.
// An empty/absent set on either side is treated as "anything", so untyped
// pins connect to everything. Otherwise the sets must share an exact MIME
// — this mirrors core.mimeCompatible (core/validate.go), the rule the
// backend enforces on submit, so the drag-create palette only ever offers
// drops the graph validator will actually accept. (No top-level category
// matching: `application/json` is NOT interchangeable with `application/x-bool`
// or `application/pdf`.)
export function mimeCompatible(a?: string[], b?: string[]): boolean {
  if (!a?.length || !b?.length) return true;
  return a.some((x) => b.some((y) => x === y));
}

// connectionHint explains, in plain language, WHY an output can't connect to
// an input — for the editor to show when it refuses a wire. Returns null when
// the connection is fine (compatible, or either pin untyped). Cardinality
// (one/many) is intentionally NOT a reason: the engine auto-lifts one→many and
// runs many→one per item, so only a KIND clash (e.g. Items into a Text input)
// is a real error, and the fix is a conversion step.
export function connectionHint(out?: Port, inp?: Port): string | null {
  if (!out || !inp) return null;
  if (mimeCompatible(out.mime, inp.mime)) return null;
  const from = portKind(out);
  const to = portKind(inp);
  if (from === "item" && to === "text") {
    return "Items can’t plug into a Text input — add a “Make text from items” drop in between.";
  }
  if (from === "text" && to === "item") {
    return "Text can’t plug into an Items input — add a “Read fields from text” drop in between.";
  }
  const noun = (k: PortKind) =>
    ({ item: "Items", text: "Text", bool: "a Yes/no", file: "a File", any: "data" })[k];
  return `${noun(from)} can’t connect to ${noun(to)} — the data types don’t match.`;
}

// portsConnectable is the ConnectionValidator decision: may a wire run from
// the source node's `sourceHandle` output to the target node's `targetHandle`
// input? It looks up each declared port and applies mimeCompatible. Either pin
// being absent (an untyped/default/exec handle, or a comment node) means
// "connectable" — the same permissive rule the editor uses so it never blocks
// a wire the backend validator would accept. Pure + node-shape-agnostic: the
// caller passes the two manifests' port lists.
export function portsConnectable(
  sourceOutputs: Port[] | undefined,
  sourceHandle: string | null | undefined,
  targetInputs: Port[] | undefined,
  targetHandle: string | null | undefined,
): boolean {
  const out = sourceOutputs?.find((p) => p.port === (sourceHandle ?? "out"));
  const inp = targetInputs?.find((p) => p.port === (targetHandle ?? "in"));
  if (!out || !inp) return true;
  return mimeCompatible(out.mime, inp.mime);
}

// pickPort chooses which port on the spawned drop to auto-wire to. The
// passthrough pin is untyped and sits first, so a naive "first compatible
// port" would always land on it — but when you drag a Text output onto a
// new ntfy node you want its "Message" input, not Pass-through. So we
// prefer a real (non-pass) input:
//
//  1. typed source — a real port whose declared MIME explicitly matches the
//     dragged port (so a json output lands on a json input, not a sibling);
//  2. untyped source (a file/blob/"any" output, e.g. a downloaded file) — a
//     real port that is ALSO untyped. An exact-MIME match is meaningless here
//     because an empty MIME set matches everything, so tier 1 would otherwise
//     grab the first *typed* field — landing a file on Gmail's "To" instead of
//     its untyped "Attachments". A blob belongs in the untyped sink;
//  3. else any compatible real port (covers a typed source → an untyped target
//     like ntfy's "Message", which has no MIME but is still the right one);
//  4. else fall back to the old behaviour — first compatible port (which may
//     be the passthrough pin), then the first declared port, then the engine's
//     default handle id.
export function pickPort(
  ports: Port[] | undefined,
  otherMime: string[] | undefined,
  fallback: string,
): string {
  if (!ports?.length) return fallback;
  const real = ports.filter((p) => p.port !== PASS_PORT);
  if (otherMime?.length) {
    const strict = real.find((p) => p.mime?.length && mimeCompatible(p.mime, otherMime));
    if (strict) return strict.port;
  } else {
    const untyped = real.find((p) => !p.mime?.length);
    if (untyped) return untyped.port;
  }
  const loose = real.find((p) => mimeCompatible(p.mime, otherMime));
  if (loose) return loose.port;
  return (ports.find((p) => mimeCompatible(p.mime, otherMime)) ?? ports[0]).port;
}
