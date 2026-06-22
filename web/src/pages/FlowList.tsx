import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Workflow, Lock, Globe, Clock, Pause, Play } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { FlowIcon, isBrandedIcon } from "../icons";
import { FlowStatusChip } from "../components/FlowStatusChip";
import { isImageIcon } from "../lib/iconImage";
import { shouldShowTenantID } from "../lib/visibleTenant";
import {
  describeSchedule,
  formatNextRun,
  summarizeFlowSchedule,
  type FlowSchedule,
} from "../lib/schedule";
import { userScope } from "../recentFlow";
import type { FlowSummary, ScheduleEntry } from "../types";

// HAS_FLOWS_KEY mirrors the key App.tsx's RootRedirect reads when
// deciding whether a bare-root visit lands on /welcome or /flows.
// Kept as a string-literal here rather than imported across the
// page boundary so both halves can run independently if the other
// fails to mount (e.g. lazy-route splitting).
const HAS_FLOWS_KEY = "dazyflow.hasFlows";

export function FlowList() {
  const { t } = useTranslation();
  const { token, me, tenants, activeTenant, activeWorkspace, hasPerm } = useAuth();
  // Creating a flow needs graph:edit; viewers can browse + open flows but
  // not create. Disable the create CTAs (with a tooltip) so they don't
  // click into a server "missing graph:edit" error. Browsing templates
  // stays enabled — only the fork/create action is gated (on Templates).
  const canEdit = hasPerm("graph:edit");
  const [flows, setFlows] = useState<FlowSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Schedules fold-in: the standalone Schedules page is gone — its
  // workspace-wide overview + per-trigger pause/resume now live on each
  // scheduled flow's card below.
  const [schedules, setSchedules] = useState<ScheduleEntry[]>([]);
  const [schedBusy, setSchedBusy] = useState<string | null>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!token || !me || !activeWorkspace) return;
    let cancelled = false;
    setLoading(true);
    api
      .listGraphs(token, activeTenant, activeWorkspace)
      .then((r) => {
        if (cancelled) return;
        const graphs = r.graphs ?? [];
        setFlows(graphs);
        // Sticky "this user has flows" hint, read by RootRedirect on
        // the next bare-root visit so a returning user skips the
        // /welcome wizard. We write the flag even on empty results
        // (with "0") so a user who deleted everything goes back to
        // the wizard rather than landing on an empty FlowList. Wrapped
        // in try/catch — localStorage might be blocked in a strict
        // iframe and a thrown error here would blank the page.
        try {
          // Per-account key — see userScope: the hint must not follow
          // a different user signing in on the same browser.
          localStorage.setItem(
            `${HAS_FLOWS_KEY}.${userScope(me)}`,
            graphs.length > 0 ? "1" : "0",
          );
        } catch {
          /* localStorage might be blocked in a strict-mode iframe */
        }
      })
      .catch((e) => {
        if (!cancelled) setError((e as Error).message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // activeTenant is read above (listGraphs) so it MUST be a dependency —
    // without it, switching to an org whose workspace has the same name never
    // re-runs this effect and the previous org's flows persist.
  }, [token, me, activeTenant, activeWorkspace]);

  // Load the workspace's schedules (cron/poll triggers + next-run preview)
  // and group them by flow. Non-blocking: a failure here just omits the
  // schedule chips, it never blanks the flow list.
  const loadSchedules = useCallback(() => {
    if (!token || !activeWorkspace) return;
    api
      .listSchedules(token, {
        tenant: activeTenant || undefined,
        workspace: activeWorkspace || undefined,
      })
      .then((r) => setSchedules(r.schedules ?? []))
      .catch(() => setSchedules([]));
  }, [token, activeTenant, activeWorkspace]);
  useEffect(loadSchedules, [loadSchedules]);

  const scheduleByFlow = useMemo(() => {
    const m = new Map<string, ScheduleEntry[]>();
    for (const s of schedules) {
      const arr = m.get(s.graph_id);
      if (arr) arr.push(s);
      else m.set(s.graph_id, [s]);
    }
    return m;
  }, [schedules]);

  // Pause/resume a flow's scheduling as a whole: if anything is currently
  // firing, pause every active trigger; otherwise resume every
  // per-trigger-paused one. (Flow-level pauses can't be lifted per-trigger,
  // so summarizeFlowSchedule already excludes those node ids.)
  const toggleFlowSchedule = async (flowId: string, sum: FlowSchedule) => {
    if (!token || schedBusy) return;
    setSchedBusy(flowId);
    const tn = activeTenant || me?.tenant || "";
    const ws = activeWorkspace || "";
    const targets = sum.active ? sum.activeNodeIds : sum.pausedNodeIds;
    const enabled = !sum.active;
    try {
      await Promise.all(
        targets.map((nodeId) =>
          api.setTriggerEnabled(token, tn, ws, flowId, nodeId, enabled),
        ),
      );
      loadSchedules();
    } catch {
      /* leave state as-is; a failed toggle simply doesn't change it */
    } finally {
      setSchedBusy(null);
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("flowList.title")}</h1>
          {/* Org name only as context, and only when the user belongs to
              more than one org. One workspace per org, so no workspace is
              shown — a lone "main" was pure noise. */}
          {shouldShowTenantID(me, tenants.length) && (
            <div className="sub">{activeTenant || me?.tenant}</div>
          )}
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          <button
            className="primary"
            onClick={() => navigate("/flows/new")}
            disabled={!canEdit}
            title={!canEdit ? t("flowList.needEdit") : undefined}
          >
            <Plus size={16} style={{ marginRight: 6 }} />
            {t("flowList.newFlow")}
          </button>
        </div>
      </div>
      {loading && <div className="card">{t("common.loading")}</div>}
      {error && <div className="card" style={{ color: "var(--danger)" }}>{error}</div>}
      {!loading && !error && flows.length === 0 && (
        <div className="card flow-empty">
          <Workflow size={28} className="flow-empty-icon" />
          <h2>{t("flowList.emptyTitle")}</h2>
          <p>{t("flowList.emptyBody")}</p>
          <div className="flow-empty-actions">
            <button
              type="button"
              className="primary"
              onClick={() => navigate("/flows/new")}
              disabled={!canEdit}
              title={!canEdit ? t("flowList.needEdit") : undefined}
            >
              {t("flowList.emptyCreateCta")}
            </button>
          </div>
        </div>
      )}
      <div className="graph-list">
        {flows.map((f) => {
          const isPrivate = f.visibility === "private";
          const ownedByMe = !!me && f.owner === me.subject;
          const displayName = f.name || f.id;
          const schedEntries = scheduleByFlow.get(f.id);
          const sched =
            schedEntries && schedEntries.length
              ? summarizeFlowSchedule(schedEntries)
              : null;
          return (
            <Link
              key={f.id}
              to={`/flows/${encodeURIComponent(f.id)}`}
              style={{ textDecoration: "none", color: "inherit" }}
            >
              <div className="graph-card">
                <div className="name">
                  <FlowIcon
                    icon={f.icon}
                    size={isBrandedIcon(f.icon) || isImageIcon(f.icon) ? 20 : 16}
                  />
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span style={{ display: "block" }}>{displayName}</span>
                  </span>
                  {f.run_status && (
                    <FlowStatusChip status={f.run_status} size="sm" />
                  )}
                  {isPrivate ? (
                    <span
                      className="vis-badge private"
                      title={
                        ownedByMe
                          ? t("flowList.privateOwnedByYou")
                          : t("flowList.privateOwnedBy", {
                              owner: f.owner ?? t("common.unknownParen"),
                            })
                      }
                    >
                      <Lock size={11} />
                      {t("common.private")}
                    </span>
                  ) : (
                    <span
                      className="vis-badge org"
                      title={t("flowList.orgTooltip")}
                    >
                      <Globe size={11} />
                      {t("common.org")}
                    </span>
                  )}
                </div>
                {sched && (
                  <FlowScheduleChip
                    sum={sched}
                    canEdit={canEdit}
                    busy={schedBusy === f.id}
                    onToggle={() => toggleFlowSchedule(f.id, sched)}
                  />
                )}
                {f.description && (
                  <div
                    className="meta"
                    style={{ color: "var(--muted)", lineHeight: 1.4 }}
                  >
                    {f.description}
                  </div>
                )}
                {f.owner && (
                  <div className="meta" style={{ color: "var(--muted)" }}>
                    {t("flowList.createdBy", { owner: f.owner })}
                  </div>
                )}
              </div>
            </Link>
          );
        })}
      </div>
    </div>
  );
}

// FlowScheduleChip is the per-flow schedule summary that replaced the
// standalone Schedules page: the cadence, the next run (or paused state),
// and an inline pause/resume that flips every trigger on the flow at once.
// The card is a <Link>, so the toggle stops click propagation to avoid
// navigating. The toggle is gated on graph:edit — viewers just read status.
function FlowScheduleChip({
  sum,
  canEdit,
  busy,
  onToggle,
}: {
  sum: FlowSchedule;
  canEdit: boolean;
  busy: boolean;
  onToggle: () => void;
}) {
  const { t } = useTranslation();
  const cadence = describeSchedule(sum.entries[0], t);
  let statusText: string;
  let resume = false;
  let showToggle = false;
  if (sum.flowDisabled) {
    statusText = t("schedules.flowPaused");
  } else if (sum.active) {
    statusText = sum.nextRun
      ? t("flowList.nextRun", { when: formatNextRun(sum.nextRun) })
      : t("schedules.never");
    showToggle = canEdit;
  } else {
    statusText = t("schedules.paused");
    resume = true;
    showToggle = canEdit;
  }
  return (
    <div className={"flow-schedule" + (sum.active ? "" : " is-paused")}>
      <Clock size={12} className="flow-schedule-icon" />
      <span className="flow-schedule-cadence">{cadence}</span>
      <span className="flow-schedule-status">{statusText}</span>
      {showToggle && (
        <button
          type="button"
          className="flow-schedule-toggle"
          disabled={busy}
          title={resume ? t("schedules.resume") : t("schedules.pause")}
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onToggle();
          }}
        >
          {resume ? <Play size={12} /> : <Pause size={12} />}
          {resume ? t("schedules.resume") : t("schedules.pause")}
        </button>
      )}
    </div>
  );
}
