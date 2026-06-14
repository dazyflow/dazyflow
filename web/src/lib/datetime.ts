// Shared timestamp formatting. The daemon emits RFC3339 (UTC, e.g.
// "2026-06-07T20:00:00Z"); the UI renders everything in the viewer's LOCAL
// timezone, in a single standard format — "YYYY-MM-DD HH:MM" (24-hour). Using
// the Date object's local getters converts a stored UTC instant to the
// viewer's wall clock, so cron / schedule fire-times show in local time too,
// not the zone the cron was authored in.

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
