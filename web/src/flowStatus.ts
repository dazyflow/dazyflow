// Mirror of core.FlowRunStatusOf (core/flowstatus.go). Keep the two in
// lockstep: this drives the editor/Triggers-modal status chip, while the
// flow list reads the server-computed `run_status` from the same Go logic.
import type { Graph, Node as HazyGraphNode } from "./types";

export type FlowRunStatus = "live" | "manual" | "paused";

// Matches core.MaxPollIntervalSeconds — the scheduler ignores intervals
// above this, so they don't count as "live".
const MAX_POLL_INTERVAL_SECONDS = 366 * 24 * 60 * 60;

// readNumber mirrors core.paramInt: only a real number counts. A string
// "60" is deliberately NOT a configured interval — the scheduler's
// paramSeconds rejects strings, so the chip must too, or it would claim
// "live" for a flow that never fires.
function readNumber(
  params: Record<string, unknown> | undefined,
  key: string,
): number | undefined {
  const v = params?.[key];
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

function readString(
  params: Record<string, unknown> | undefined,
  key: string,
): string {
  const v = params?.[key];
  return typeof v === "string" ? v : "";
}

// hasConfiguredAutoTrigger reports whether anything will fire the flow
// without a manual Run. Rules mirror HasConfiguredAutoTrigger in Go.
export function hasConfiguredAutoTrigger(
  triggers: Graph["triggers"],
  nodes: Pick<HazyGraphNode, "module" | "params">[],
): boolean {
  for (const tr of triggers ?? []) {
    if (tr.type === "cron" && (tr.cron ?? "").trim() !== "") return true;
  }
  for (const n of nodes) {
    switch (n.module) {
      case "cron_trigger":
        if (readString(n.params, "cron").trim() !== "") return true;
        break;
      case "poll_trigger":
      case "google_form_trigger": {
        const secs = readNumber(n.params, "interval_seconds");
        if (secs !== undefined && secs > 0 && secs <= MAX_POLL_INTERVAL_SECONDS)
          return true;
        break;
      }
      case "webhook_input":
        if (
          readString(n.params, "secret").trim() !== "" ||
          n.params?.public_form === true
        )
          return true;
        break;
    }
  }
  return false;
}

// flowRunStatus classifies a flow from its parts. Disabled wins (it
// suspends all automatic firing); otherwise a configured trigger is
// "live" and its absence is "manual".
export function flowRunStatus(
  disabled: boolean | undefined,
  triggers: Graph["triggers"],
  nodes: Pick<HazyGraphNode, "module" | "params">[],
): FlowRunStatus {
  if (disabled) return "paused";
  return hasConfiguredAutoTrigger(triggers, nodes) ? "live" : "manual";
}
