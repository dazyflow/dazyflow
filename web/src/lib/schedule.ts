import { formatDateTime } from "./datetime";
import type { ScheduleEntry } from "../types";

type TFunc = (k: string, o?: Record<string, unknown>) => string;

// describeSchedule renders a human-readable cadence: the raw cron
// expression for cron triggers, or "every N <unit>" for poll triggers.
// (Moved out of the old standalone Schedules page when its overview +
// pause/resume folded into the flow list.)
export function describeSchedule(s: ScheduleEntry, t: TFunc): string {
  if (s.kind === "cron") return s.cron || "—";
  const secs = s.interval_seconds ?? 0;
  if (secs % 3600 === 0) return t("schedules.everyHours", { count: secs / 3600 });
  if (secs % 60 === 0) return t("schedules.everyMinutes", { count: secs / 60 });
  return t("schedules.everySeconds", { count: secs });
}

// formatNextRun renders an RFC3339 UTC instant in the viewer's local time.
// next_fires are UTC, so a cron authored in another timezone still shows
// in the reader's own clock.
export function formatNextRun(iso: string): string {
  return formatDateTime(iso);
}

// FlowSchedule is the per-flow rollup the flow list shows. One flow can
// carry several trigger nodes, so we collapse them into a single status
// plus the soonest upcoming run, and remember which node ids to flip when
// the user pauses/resumes the flow's scheduling as a whole.
export type FlowSchedule = {
  entries: ScheduleEntry[];
  flowDisabled: boolean; // whole-flow pause (overrides per-trigger state)
  active: boolean; // at least one trigger currently firing
  nextRun?: string; // soonest upcoming fire across the active triggers
  activeNodeIds: string[]; // currently enabled → can be paused
  pausedNodeIds: string[]; // per-trigger paused → can be resumed
};

export function summarizeFlowSchedule(entries: ScheduleEntry[]): FlowSchedule {
  const flowDisabled =
    entries.length > 0 && entries.every((e) => e.flow_disabled);
  const activeNodeIds: string[] = [];
  const pausedNodeIds: string[] = [];
  let nextRun: string | undefined;
  for (const e of entries) {
    // A flow-level pause can't be lifted per-trigger (mirrors the old
    // Schedules page hiding the toggle in that case), so skip those nodes.
    if (e.flow_disabled) continue;
    if (e.disabled) {
      pausedNodeIds.push(e.node_id);
      continue;
    }
    activeNodeIds.push(e.node_id);
    // next_fires are RFC3339, which sorts correctly lexicographically, so
    // a string compare finds the soonest without parsing.
    const first = e.next_fires?.[0];
    if (first && (!nextRun || first < nextRun)) nextRun = first;
  }
  return {
    entries,
    flowDisabled,
    active: activeNodeIds.length > 0,
    nextRun,
    activeNodeIds,
    pausedNodeIds,
  };
}
