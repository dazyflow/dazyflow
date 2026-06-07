// Shared timestamp formatting. The daemon emits RFC3339 (UTC, e.g.
// "2026-06-07T20:00:00Z"); the UI must render in the viewer's LOCAL zone.
// Relative strings ("3m ago") are zone-agnostic, but exact-time tooltips and
// absolute displays must go through the locale formatter — never the raw ISO,
// which reads as UTC and is the "why is this time wrong?" trap.

// absoluteTime renders an ISO timestamp as the locale's full date+time in the
// viewer's local zone — the right value for an exact-time tooltip. Falls back
// to the raw string if it can't be parsed, so a bad value still shows something.
export function absoluteTime(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}
