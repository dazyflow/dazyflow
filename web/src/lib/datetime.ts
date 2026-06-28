// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Shared timestamp formatting. The daemon emits RFC3339 (UTC, e.g.
// "2026-06-07T20:00:00Z"); the UI renders everything in the viewer's LOCAL
// timezone, in a single standard format — "YYYY-MM-DD HH:MM" (24-hour). Using
// the Date object's local getters converts a stored UTC instant to the
// viewer's wall clock, so cron / schedule fire-times show in local time too,
// not the zone the cron was authored in.

type TFunc = (k: string, o?: Record<string, unknown>) => string;

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

function toDate(value: string | Date | null | undefined): Date | null {
  if (value === null || value === undefined || value === "") return null;
  const d = value instanceof Date ? value : new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

// formatDateTime renders a timestamp as "YYYY-MM-DD HH:MM" in local time.
// Falls back to the raw string when it can't be parsed, so a bad value still
// shows something rather than an empty cell.
export function formatDateTime(value: string | Date | null | undefined): string {
  const d = toDate(value);
  if (!d) return typeof value === "string" ? value : "";
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// formatDate is the date-only variant: "YYYY-MM-DD", local time.
export function formatDate(value: string | Date | null | undefined): string {
  const d = toDate(value);
  if (!d) return typeof value === "string" ? value : "";
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

// formatClock is the time-only variant: "HH:MM", local time.
export function formatClock(value: string | Date | null | undefined): string {
  const d = toDate(value);
  if (!d) return typeof value === "string" ? value : "";
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// absoluteTime is the legacy name kept for existing callers; it now returns
// the standard local "YYYY-MM-DD HH:MM" like everything else.
export function absoluteTime(value: string | Date | null | undefined): string {
  return formatDateTime(value);
}

// formatRelative renders a coarse "time ago" string ("just now", "5m ago",
// "3h ago", "2d ago") for recency-at-a-glance surfaces — the flow cards'
// last-run line and the runs table. Strings are localized via `t` (the same
// pattern as formatNextRun) so a Swedish viewer doesn't see English "ago"
// fragments. Past the one-week mark it hands off to the absolute date, where
// "2026-04-03" reads better than "37d ago". `now` is injectable so tests
// don't depend on the wall clock. Returns "" for an unparseable/empty value
// so callers can hide the line.
export function formatRelative(
  value: string | Date | null | undefined,
  t: TFunc,
  now: Date = new Date(),
): string {
  const d = toDate(value);
  if (!d) return "";
  const secs = Math.round((now.getTime() - d.getTime()) / 1000);
  if (secs < 0) return formatDateTime(d); // clock skew / future timestamp
  if (secs < 45) return t("relative.justNow");
  const mins = Math.round(secs / 60);
  if (mins < 60) return t("relative.minutesAgo", { count: mins });
  const hours = Math.round(mins / 60);
  if (hours < 24) return t("relative.hoursAgo", { count: hours });
  const days = Math.round(hours / 24);
  if (days <= 7) return t("relative.daysAgo", { count: days });
  return formatDate(d);
}
