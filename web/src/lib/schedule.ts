// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { formatClock, formatDateTime } from "./datetime";
import type { ScheduleEntry } from "../types";

type TFunc = (k: string, o?: Record<string, unknown>) => string;

export function describeSchedule(s: ScheduleEntry, t: TFunc): string {
  if (s.kind === "cron") return s.cron || "—";
  const secs = s.interval_seconds ?? 0;
  if (secs % 3600 === 0) return t("schedules.everyHours", { count: secs / 3600 });
  if (secs % 60 === 0) return t("schedules.everyMinutes", { count: secs / 60 });
  return t("schedules.everySeconds", { count: secs });
}

// sameLocalDay reports whether two instants fall on the same calendar day
// in the viewer's local timezone.
function sameLocalDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

// formatNextRun renders an RFC3339 UTC instant in the viewer's local time,
// but leans on relative phrasing when it reads more naturally: "in N minutes"
// when under an hour away, "Today HH:MM" / "Tomorrow HH:MM" for the next two
// calendar days, and the full "YYYY-MM-DD HH:MM" only further out. next_fires
// are UTC, so a cron authored in another timezone still shows in local time.
export function formatNextRun(iso: string, t: TFunc): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return formatDateTime(iso);

  const now = new Date();
  const diffMs = d.getTime() - now.getTime();

  const mins = Math.round(diffMs / 60000);
  if (diffMs >= 0 && mins < 60) {
    if (mins < 1) return t("schedules.relSoon");
    return t("schedules.relInMinutes", { count: mins });
  }

  const time = formatClock(d);
  if (sameLocalDay(d, now)) return t("schedules.relToday", { time });

  const tomorrow = new Date(now);
  tomorrow.setDate(now.getDate() + 1);
  if (sameLocalDay(d, tomorrow)) return t("schedules.relTomorrow", { time });

  return formatDateTime(iso);
}

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
