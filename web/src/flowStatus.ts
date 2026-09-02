// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Mirror of core.FlowRunStatusOf (core/flowstatus.go). Keep the two in
// lockstep: this drives the editor/Triggers-modal status chip, while the
// flow list reads the server-computed `run_status` from the same Go logic.
import type { Graph, Node as DazyGraphNode } from "./types";

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

// EVENT_TRIGGER_MODULES mirrors core.EventTriggerModules — trigger drops fired
// by an inbound provider event rather than the scheduler or the /trigger
// webhook. Kept in lockstep by TestEventTriggerModulesMatchCatalog on the Go
// side; add a drop there and mirror it here.
const EVENT_TRIGGER_MODULES = new Set([
  "slack_on_mention",
  "github_on_push",
  "github_on_new_pr",
  "stripe_on_payment",
  "stripe_on_payment_failed",
  "stripe_on_subscription_canceled",
  "homeassistant_state_changed",
]);

// hasConfiguredAutoTrigger reports whether anything will fire the flow
// without a manual Run. Rules mirror HasConfiguredAutoTrigger in Go.
function hasConfiguredAutoTrigger(
  triggers: Graph["triggers"],
  nodes: Pick<DazyGraphNode, "module" | "params">[],
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
      default:
        // Provider-event triggers (a Slack mention, a GitHub push, a Stripe
        // payment). Mirrors core.EventTriggerModules — the node's presence is
        // enough, since the daemon's fan-out matches on module ID alone.
        if (EVENT_TRIGGER_MODULES.has(n.module)) return true;
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
  nodes: Pick<DazyGraphNode, "module" | "params">[],
): FlowRunStatus {
  if (disabled) return "paused";
  return hasConfiguredAutoTrigger(triggers, nodes) ? "live" : "manual";
}

// flowRunStatusPublished is the publish-aware classifier (mirrors
// core.FlowRunStatusPublished). A flow that would fire on its own but isn't
// published yet is "needs_publish" — NO automatic path runs an unpublished
// flow. `published` is undefined while the editor is still loading publish
// info; treat undefined as published so the chip doesn't flicker to "needs
// publish" before we know.
//
// This used to apply only to scheduler triggers, because the webhook, form
// and event endpoints fell back to HEAD and so did fire while unpublished.
// That asymmetry is gone on the Go side; keep the two in lockstep.
export function flowRunStatusPublished(
  disabled: boolean | undefined,
  triggers: Graph["triggers"],
  nodes: Pick<DazyGraphNode, "module" | "params">[],
  published: boolean | undefined,
): FlowRunStatus {
  const base = flowRunStatus(disabled, triggers, nodes);
  if (base === "live" && published === false) {
    return "needs_publish";
  }
  return base;
}
