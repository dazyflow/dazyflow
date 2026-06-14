import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { CalendarClock, List, Pause, Play, Workflow } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { formatClock, formatDate, formatDateTime } from "../lib/datetime";
import type { ScheduleEntry } from "../types";

export function Schedules() {
  const { t } = useTranslation();
  const { token, me, activeTenant, activeWorkspace } = useAuth();
  const [schedules, setSchedules] = useState<ScheduleEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [view, setView] = useState<"list" | "calendar">("list");
  // node_id currently being toggled — disables its button to avoid double-fire.
  const [busy, setBusy] = useState<string | null>(null);

  const tenant = activeTenant || me?.tenant || "";
  const workspace = activeWorkspace || me?.workspace || "";

  const load = useCallback(() => {
    if (!token || !tenant || !workspace) return;
    setLoading(true);
    setError(null);
    api
      .listSchedules(token, { tenant, workspace })
      .then((r) => setSchedules(r.schedules ?? []))
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [token, tenant, workspace]);

  useEffect(() => {
    load();
  }, [load]);

  const toggle = async (e: ScheduleEntry) => {
    if (!token) return;
    const [tn, ws, id] = e.flow_id.split("/");
    setBusy(e.node_id);
    try {
      await api.setTriggerEnabled(token, tn, ws, id, e.node_id, e.disabled);
      load();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(null);
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("schedules.title")}</h1>
          <div className="sub">{t("schedules.subtitle")}</div>
        </div>
        <div className="run-history-filters">
          <button
            type="button"
            className={"run-filter-chip" + (view === "list" ? " active" : "")}
            onClick={() => setView("list")}
          >
            <List size={13} style={{ verticalAlign: -2, marginRight: 4 }} />
            {t("schedules.viewList")}
          </button>
          <button
            type="button"
            className={"run-filter-chip" + (view === "calendar" ? " active" : "")}
            onClick={() => setView("calendar")}
          >
            <CalendarClock size={13} style={{ verticalAlign: -2, marginRight: 4 }} />
            {t("schedules.viewCalendar")}
          </button>
        </div>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)" }}>
          {error}
        </div>
      )}
      {!error && loading && schedules.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      )}
      {!error && !loading && schedules.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("schedules.empty")}
        </div>
      )}

      {schedules.length > 0 && view === "calendar" && (
        <ScheduleCalendar schedules={schedules} t={t} />
      )}

      {schedules.length > 0 && view === "list" && (
        <div className="card" style={{ padding: 0, overflow: "hidden" }}>
          <table className="run-table">
            <thead>
              <tr>
                <th>{t("schedules.colFlow")}</th>
                <th>{t("schedules.colSchedule")}</th>
                <th>{t("schedules.colTimezone")}</th>
                <th>{t("schedules.colNextRun")}</th>
                <th>{t("schedules.colStatus")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {schedules.map((s) => {
                const paused = s.disabled || s.flow_disabled;
                return (
                  <tr key={`${s.flow_id}@${s.node_id}`}>
                    <td>
                      <Link
                        to={`/flows/${encodeURIComponent(s.graph_id)}`}
                        style={{ display: "inline-flex", alignItems: "center", gap: 4 }}
                      >
                        <Workflow size={12} />
                        {s.flow_name || s.graph_id}
                      </Link>
                    </td>
                    <td style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)" }}>
                      {describeSchedule(s, t)}
                    </td>
                    <td style={{ color: "var(--muted)", fontSize: "var(--text-sm)" }}>
                      {s.kind === "cron" ? s.tz || "UTC" : "—"}
                    </td>
                    <td style={{ color: "var(--muted)", fontSize: "var(--text-sm)" }}>
                      {paused
                        ? "—"
                        : s.next_fires && s.next_fires.length > 0
                        ? formatNextRun(s.next_fires[0])
                        : t("schedules.never")}
                    </td>
                    <td>
                      <span className={"status-chip " + (paused ? "paused" : "live")}>
                        {s.flow_disabled
                          ? t("schedules.flowPaused")
                          : s.disabled
                          ? t("schedules.paused")
                          : t("schedules.active")}
                      </span>
                    </td>
                    <td style={{ textAlign: "right", paddingRight: 12 }}>
                      {/* When the whole flow is paused, the per-trigger toggle
                          is meaningless — resume the flow from its editor /
                          flow list instead, so we hide the action here. */}
                      {!s.flow_disabled && (
                        <button
                          type="button"
                          className="btn-ghost"
                          disabled={busy === s.node_id}
                          onClick={() => toggle(s)}
                          title={s.disabled ? t("schedules.resume") : t("schedules.pause")}
                          style={{ display: "inline-flex", alignItems: "center", gap: 4 }}
                        >
                          {s.disabled ? <Play size={14} /> : <Pause size={14} />}
                          {s.disabled ? t("schedules.resume") : t("schedules.pause")}
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <div
        className="sub"
        style={{ marginTop: "var(--space-4)", display: "flex", alignItems: "center", gap: 6 }}
      >
        <CalendarClock size={13} />
        {t("schedules.footnote")}
      </div>
    </div>
  );
}

// ScheduleCalendar lays out each trigger's upcoming fires (from
// next_fires) on a 7-day timeline, one column per day. Pure client
// rendering — no extra backend call. Paused triggers carry no next_fires
// so they simply don't appear.
function ScheduleCalendar({
  schedules,
  t,
}: {
  schedules: ScheduleEntry[];
  t: (k: string, o?: Record<string, unknown>) => string;
}) {
  // Build 7 day-buckets starting today (local midnight).
  const days: { date: Date; key: string; fires: { when: Date; label: string }[] }[] = [];
  const start = new Date();
  start.setHours(0, 0, 0, 0);
  for (let i = 0; i < 7; i++) {
    const d = new Date(start);
    d.setDate(start.getDate() + i);
    days.push({ date: d, key: d.toDateString(), fires: [] });
  }
  const byKey = new Map(days.map((d) => [d.key, d]));
  for (const s of schedules) {
    for (const iso of s.next_fires ?? []) {
      const when = new Date(iso);
      if (Number.isNaN(when.getTime())) continue;
      const bucket = byKey.get(when.toDateString());
      if (bucket) bucket.fires.push({ when, label: s.flow_name || s.graph_id });
    }
  }
  for (const d of days) d.fires.sort((a, b) => a.when.getTime() - b.when.getTime());

  return (
    <div className="schedule-calendar">
      {days.map((d) => (
        <div key={d.key} className="schedule-cal-day">
          <div className="schedule-cal-head">{formatDate(d.date)}</div>
          {d.fires.length === 0 ? (
            <div className="schedule-cal-empty">{t("schedules.noFires")}</div>
          ) : (
            d.fires.map((f, i) => (
              <div key={i} className="schedule-cal-fire" title={f.label}>
                <span className="schedule-cal-time">{formatClock(f.when)}</span>{" "}
                <span className="schedule-cal-flow">{f.label}</span>
              </div>
            ))
          )}
        </div>
      ))}
    </div>
  );
}

// describeSchedule renders a human-readable cadence: the raw cron
// expression for cron triggers, or "every N <unit>" for poll triggers.
function describeSchedule(
  s: ScheduleEntry,
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  if (s.kind === "cron") return s.cron || "—";
  const secs = s.interval_seconds ?? 0;
  if (secs % 3600 === 0) return t("schedules.everyHours", { count: secs / 3600 });
  if (secs % 60 === 0) return t("schedules.everyMinutes", { count: secs / 60 });
  return t("schedules.everySeconds", { count: secs });
}

// formatNextRun renders the next fire as standard local "YYYY-MM-DD HH:MM".
// next_fires are UTC instants; the local formatter shows them in the
// viewer's own clock — so a cron authored in another timezone still
// displays in local time.
function formatNextRun(iso: string): string {
  return formatDateTime(iso);
}
