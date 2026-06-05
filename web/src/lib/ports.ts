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
// pins connect to everything.
export function mimeCompatible(a?: string[], b?: string[]): boolean {
  if (!a?.length || !b?.length) return true;
  return a.some((x) => b.some((y) => x === y || x.split("/")[0] === y.split("/")[0]));
}

// pickPort chooses which port on the spawned drop to auto-wire to. The
// passthrough pin is untyped and sits first, so a naive "first compatible
// port" would always land on it — but when you drag a Text output onto a
// new ntfy node you want its "Message" input, not Pass-through. So we
// prefer a real (non-pass) input:
//
//  1. a real port whose declared MIME explicitly matches the dragged port;
//  2. else any compatible real port (covers untyped pins like ntfy's
//     "Message", which has no MIME but is still the right target);
//  3. else fall back to the old behaviour — first compatible port (which
//     may be the passthrough pin), then the first declared port, then the
//     engine's default handle id.
export function pickPort(
  ports: Port[] | undefined,
  otherMime: string[] | undefined,
  fallback: string,
): string {
  if (!ports?.length) return fallback;
  const real = ports.filter((p) => p.port !== PASS_PORT);
  const strict = real.find((p) => p.mime?.length && mimeCompatible(p.mime, otherMime));
  if (strict) return strict.port;
  const loose = real.find((p) => mimeCompatible(p.mime, otherMime));
  if (loose) return loose.port;
  return (ports.find((p) => mimeCompatible(p.mime, otherMime)) ?? ports[0]).port;
}
