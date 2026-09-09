// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// A skipped step went grey for a reason, and the reason is the difference
// between "this is fine" and "why did nothing happen?". The run stream and the
// step's record carry a stable code (core.SkipCode* in Go); the copy lives here,
// per language, and is shared by the canvas card and the run detail.

export type SkipCopy = { label: string; hint: string };

// Mirrors core.SkipCode* — add a code there and give it copy here, or both
// views fall back to a bare "Skipped".
const SKIP_COPY: Record<string, SkipCopy> = {
  trigger_not_fired: { label: "skip.notFired", hint: "skip.notFiredHint" },
  upstream: { label: "skip.upstream", hint: "skip.upstreamHint" },
  step_off: { label: "skip.stepOff", hint: "skip.stepOffHint" },
};

// skipCopy returns the i18n keys explaining one skip. An unknown or absent code
// still gets copy: the step demonstrably did not run, and saying that much beats
// saying nothing.
export function skipCopy(code: string | undefined): SkipCopy {
  return (code && SKIP_COPY[code]) || { label: "skip.skipped", hint: "skip.skippedHint" };
}

// The canvas card draws no chip for a switched-off step, which already carries
// its own "Off" chip — two chips saying the same thing is worse than one. The
// run detail has no such chip, so it explains every skip.
export function cardSkipCopy(
  status: string | undefined,
  code: string | undefined,
): SkipCopy | null {
  if (status !== "skipped") return null;
  if (code === "step_off") return null;
  return skipCopy(code);
}
