// Mirror of core.FlowRunStatusOf (core/flowstatus.go). Keep the two in
// lockstep: this drives the editor/Triggers-modal status chip, while the
// flow list reads the server-computed `run_status` from the same Go logic.
import type { Graph, Node as HazyGraphNode } from "./types";

export type FlowRunStatus = "live" | "manual" | "paused" | "needs_publish";

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

// webhookKeys returns every bearer key configured on a webhook trigger:
// the `secrets` list, each non-blank, in order. Mirrors core.WebhookSecrets
// (Go). The /trigger endpoint accepts ANY of them, which is what lets the
// editor add a key, migrate callers, then revoke the old one with zero
// downtime. The webhook UI and status both read through this.
export function webhookKeys(
  src: { secrets?: unknown } | undefined,
): string[] {
  const out: string[] = [];
  const add = (v: unknown) => {
    if (typeof v === "string" && v.trim() !== "") out.push(v);
  };
  const arr = src?.secrets;
  if (Array.isArray(arr)) arr.forEach(add);
  return out;
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
        if (webhookKeys(n.params).length > 0 || n.params?.public_form === true)
          return true;
        break;
    }
  }
  return false;
}

// hasConfiguredSchedulerTrigger reports whether the flow has a configured
// trigger that fires via the SCHEDULER (cron / poll / google-form interval) —
// the subset of auto-triggers gated on publish state. Webhooks fire from an
// HTTP endpoint and don't require publishing, so they're excluded. Mirrors
// core.HasConfiguredSchedulerTrigger (Go).
export function hasConfiguredSchedulerTrigger(
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

// flowRunStatusPublished is the publish-aware classifier (mirrors
// core.FlowRunStatusPublished). A flow that would be live via a SCHEDULER
// trigger but isn't published yet is "needs_publish" — the scheduler only
// runs published flows. `published` is undefined while the editor is still
// loading publish info; treat undefined as published so the chip doesn't
// flicker to "needs publish" before we know.
export function flowRunStatusPublished(
  disabled: boolean | undefined,
  triggers: Graph["triggers"],
  nodes: Pick<HazyGraphNode, "module" | "params">[],
  published: boolean | undefined,
): FlowRunStatus {
  const base = flowRunStatus(disabled, triggers, nodes);
  if (
    base === "live" &&
    published === false &&
    hasConfiguredSchedulerTrigger(triggers, nodes)
  ) {
    return "needs_publish";
  }
  return base;
}
