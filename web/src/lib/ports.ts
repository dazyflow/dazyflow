import type { Port } from "../types";

// Port-matching helpers for auto-wiring when a drag-create spawns a new
// node. Pure functions, extracted from FlowEditor so they can be unit
// tested.

// The passthrough pin ("pass") is prepended to every processing drop's
// inputs and outputs by core.WithPassthrough. It's untyped, so it matches
// any MIME — which is exactly why it must not be auto-chosen by default.
export const PASS_PORT = "pass";

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
