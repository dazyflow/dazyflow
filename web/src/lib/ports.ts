// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Port } from "../types";


// Prepended by the server, so it is not in the drop's declared port list.
export const PASS_PORT = "pass";

// Mirrors core.PortKind; keep the two in step.
export type PortKind = "item" | "text" | "bool" | "file" | "any";

export function portKind(p: Pick<Port, "mime">): PortKind {
  const m = p.mime;
  if (!m?.length) return "any";
  if (m.includes("application/json") || m.includes("application/x-dazyflow-list+json")) return "item";
  if (m.includes("text/plain") || m.includes("text/html")) return "text";
  if (m.includes("application/x-bool")) return "bool";
  return "file";
}

export function portCardinality(p: Pick<Port, "list">): "one" | "many" {
  return p.list ? "many" : "one";
}

type Translate = (key: string, defaultValue: string) => string;

export function portTypeLabel(
  p: Pick<Port, "mime" | "list">,
  t?: Translate,
): string {
  const tr: Translate = t ?? ((_k, d) => d);
  const many = portCardinality(p) === "many";
  switch (portKind(p)) {
    case "item":
      return many
        ? tr("portType.items", "Items (a table)")
        : tr("portType.item", "Item");
    case "text":
      return many ? tr("portType.texts", "Texts") : tr("portType.text", "Text");
    case "bool":
      return tr("portType.bool", "Yes / no");
    case "file":
      return many ? tr("portType.files", "Files") : tr("portType.file", "File");
    default:
      return tr("portType.any", "Anything");
  }
}

// An empty set on either side is a wildcard, matching the server's rule.
export function mimeCompatible(a?: string[], b?: string[]): boolean {
  if (!a?.length || !b?.length) return true;
  return a.some((x) => b.some((y) => x === y));
}

// A refused wire must say why, or the author retries the same drag.
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

// Must agree with the server's validation, or the editor allows an unsavable flow.
export function portsConnectable(
  sourceOutputs: Port[] | undefined,
  sourceHandle: string | null | undefined,
  targetInputs: Port[] | undefined,
  targetHandle: string | null | undefined,
): boolean {
  // A drop that declares its ports and none on this side cannot take a wire.
  if (targetInputs?.length === 0) return false;
  const out = sourceOutputs?.find((p) => p.port === (sourceHandle ?? "out"));
  const inp = targetInputs?.find((p) => p.port === (targetHandle ?? "in"));
  if (!out || !inp) return true;
  return mimeCompatible(out.mime, inp.mime);
}

// Mirrors core.DefaultMaxVariadicFanIn.
export const DEFAULT_MAX_VARIADIC_FAN_IN = 64;

// Mirrors core.MaxVariadicFanIn: the ceiling no manifest can raise.
export const MAX_VARIADIC_FAN_IN = 1024;

// A single-value input takes ONE wire; a variadic one respects its declared max.
export function inputHasRoom(
  targetInputs: Port[] | undefined,
  targetHandle: string | null | undefined,
  existing: number,
  dynamicPorts = false,
  catalogued = true,
): boolean {
  const inp = targetInputs?.find((p) => p.port === (targetHandle ?? "in"));
  if (!inp) return dynamicPorts || !catalogued ? existing < 1 : true;
  if (!inp.variadic) return existing < 1;
  return existing < Math.min(inp.max ?? DEFAULT_MAX_VARIADIC_FAN_IN, MAX_VARIADIC_FAN_IN);
}

// Type compatibility first, then declaration order, so the pick is predictable.
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

// The wire the author already started must land somewhere sensible.
export function spawnPort(
  ports: Port[] | undefined,
  otherMime: string[] | undefined,
  fromPass: boolean,
  fallback: string,
): string | null {
  if (!ports?.length) return null;
  if (fromPass && ports.some((p) => p.port === PASS_PORT)) return PASS_PORT;
  return pickPort(ports, otherMime, fallback);
}
