// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  Plus,
  Workflow,
  Lock,
  Clock,
  Pause,
  Play,
  Search,
  Trash2,
  Copy,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { FLOWS_CHANGED_EVENT } from "../activeFlow";
import { Button } from "../components/Button";
import { DeleteFlowModal } from "../components/DeleteFlowModal";
import { DuplicateFlowModal } from "../components/DuplicateFlowModal";
import { FlowIcon, isBrandedIcon } from "../icons";
import { FlowStatusChip } from "../components/FlowStatusChip";
import { RunSparkline } from "../components/RunSparkline";
import { isImageIcon } from "../lib/iconImage";
import { formatRelative, formatDateTime } from "../lib/datetime";
import {
  formatNextRun,
  summarizeFlowSchedule,
  type FlowSchedule,
} from "../lib/schedule";
import { userScope } from "../recentFlow";
import { explainApiError } from "../lib/explainApiError";
import type { FlowRunStatus } from "../flowStatus";
import type { FlowSummary, RunSummary, ScheduleEntry } from "../types";

type SortKey = "recent" | "name" | "status";

// Order flows fall in when sorting by run status: live (firing on its own)
// first, paused last — the order an operator scans for "what's actually
// running" vs "what's switched off".
const STATUS_ORDER: Record<FlowRunStatus, number> = {
  live: 0,
  needs_publish: 1,
  manual: 2,
  paused: 3,
};

// HAS_FLOWS_KEY mirrors the key App.tsx's RootRedirect reads when
// deciding whether a bare-root visit lands on /welcome or /flows.
// Kept as a string-literal here rather than imported across the
// page boundary so both halves can run independently if the other
// fails to mount (e.g. lazy-route splitting).
const HAS_FLOWS_KEY = "dazyflow.hasFlows";

export function FlowList() {
  const { t } = useTranslation();
  const { token, me, activeTenant, activeWorkspace, hasPerm } = useAuth();
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
  // Recent runs across the workspace, grouped per flow — drives each card's
  // last-run line and health sparkline. Non-blocking: a failure here just
  // leaves the cards without the run signal, it never blanks the list.
  const [runsByFlow, setRunsByFlow] = useState<Map<string, RunSummary[]>>(
    new Map(),
  );
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<SortKey>("recent");
  const [statusFilter, setStatusFilter] = useState<FlowRunStatus | "all">("all");
  // The flow whose password-gated delete confirm is open (null = none).
  const [deleteTarget, setDeleteTarget] = useState<FlowSummary | null>(null);
  // The flow whose duplicate name-prompt is open (null = none).
  const [dupTarget, setDupTarget] = useState<FlowSummary | null>(null);
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
          // Per-(account, active org) key — see userScope: the hint must
          // not follow a different user signing in on the same browser, nor
          // carry one org's "has flows" state into another after a switch.
          localStorage.setItem(
            `${HAS_FLOWS_KEY}.${userScope(activeTenant || me?.tenant, me?.subject)}`,
            graphs.length > 0 ? "1" : "0",
          );
        } catch {
          /* localStorage might be blocked in a strict-mode iframe */
        }
      })
      .catch((e) => {
        if (!cancelled) setError(explainApiError(e, t));
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
  }, [token, me, activeTenant, activeWorkspace, t]);

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

  // Pull the most recent runs workspace-wide and bucket them by flow. One
  // request feeds every card's last-run line + sparkline (vs N per-flow
  // requests); the API returns newest-first, which both consumers rely on.
  useEffect(() => {
    if (!token || !activeWorkspace) return;
    let cancelled = false;
    api
      .listAllRuns(token, {
        tenant: activeTenant || undefined,
        workspace: activeWorkspace || undefined,
        limit: 200,
      })
      .then((r) => {
        if (cancelled) return;
        const m = new Map<string, RunSummary[]>();
        for (const run of r.runs ?? []) {
          const arr = m.get(run.graph_id);
          if (arr) arr.push(run);
          else m.set(run.graph_id, [run]);
        }
        setRunsByFlow(m);
      })
      .catch(() => {
        if (!cancelled) setRunsByFlow(new Map());
      });
    return () => {
      cancelled = true;
    };
  }, [token, activeTenant, activeWorkspace]);

  // The flows actually rendered: text-filtered, status-filtered, and sorted
  // per the toolbar. Derived (not state) so it always tracks the inputs.
  const visibleFlows = useMemo(() => {
    const q = query.trim().toLowerCase();
    const matchesQuery = (f: FlowSummary) =>
      !q ||
      (f.name ?? "").toLowerCase().includes(q) ||
      f.id.toLowerCase().includes(q) ||
      (f.description ?? "").toLowerCase().includes(q);
    const matchesStatus = (f: FlowSummary) =>
      statusFilter === "all" || f.run_status === statusFilter;
    const lastRunAt = (f: FlowSummary): number => {
      const last = runsByFlow.get(f.id)?.[0];
      const ts = last?.started_at || last?.enqueued_at;
      return ts ? new Date(ts).getTime() : 0;
    };
    const filtered = flows.filter((f) => matchesQuery(f) && matchesStatus(f));
    const sorted = [...filtered];
    sorted.sort((a, b) => {
      if (sort === "name")
        return (a.name || a.id).localeCompare(b.name || b.id);
      if (sort === "status")
        return (
          (STATUS_ORDER[a.run_status as FlowRunStatus] ?? 9) -
          (STATUS_ORDER[b.run_status as FlowRunStatus] ?? 9)
        );
      // recent: most recently run first; never-run flows sink to the bottom.
      return lastRunAt(b) - lastRunAt(a);
    });
    return sorted;
  }, [flows, query, statusFilter, sort, runsByFlow]);

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

  // deleteFlow permanently removes a flow after the password confirm. On
  // success we drop it from the in-memory list (no refetch) and keep the
  // "has flows" hint in sync so a user who just deleted their last flow lands
  // back on the welcome wizard rather than an empty list. A thrown error
  // propagates to DeleteFlowModal, which shows it and stays open.
  const deleteFlow = async (target: FlowSummary, password: string) => {
    if (!token) return;
    const tn = activeTenant || me?.tenant || "";
    const ws = activeWorkspace || "";
    await api.deleteGraph(token, tn, ws, target.id, password);
    setFlows((prev) => {
      const next = prev.filter((f) => f.id !== target.id);
      try {
        if (me) {
          localStorage.setItem(
            `${HAS_FLOWS_KEY}.${userScope(activeTenant || me?.tenant, me?.subject)}`,
            next.length > 0 ? "1" : "0",
          );
        }
      } catch {
        /* localStorage might be blocked in a strict-mode iframe */
      }
      return next;
    });
    setDeleteTarget(null);
    // Tell the sidebar (and anything else listening) the flow set changed, so
    // its own list drops the deleted flow without waiting for a navigation.
    window.dispatchEvent(new Event(FLOWS_CHANGED_EVENT));
  };

  // duplicateFlow creates an independent copy under the chosen name and opens
  // it in the editor. The copy is a DISABLED draft (the daemon's contract), so
  // the user lands on something safe to review before enabling. A thrown error
  // propagates to DuplicateFlowModal, which shows it and stays open.
  const duplicateFlow = async (target: FlowSummary, name: string) => {
    if (!token) return;
    const tn = activeTenant || me?.tenant || "";
    const ws = activeWorkspace || "";
    const newID = await api.duplicateFlow(token, tn, ws, target.id, name);
    setDupTarget(null);
    // The flow set changed; let the sidebar refresh in the background.
    window.dispatchEvent(new Event(FLOWS_CHANGED_EVENT));
    navigate(`/flows/${encodeURIComponent(newID)}`);
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("flowList.title")}</h1>
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          <Button
            variant="primary"
            onClick={() => navigate("/flows/new")}
            disabled={!canEdit}
            title={!canEdit ? t("flowList.needEdit") : undefined}
          >
            <Plus size={16} style={{ marginRight: 6 }} />
            {t("flowList.newFlow")}
          </Button>
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
            <Button
              variant="primary"
              onClick={() => navigate("/flows/new")}
              disabled={!canEdit}
              title={!canEdit ? t("flowList.needEdit") : undefined}
            >
              {t("flowList.emptyCreateCta")}
            </Button>
          </div>
        </div>
      )}
      {/* Search / sort / filter toolbar — only once there are enough flows
          to need it. A two-flow workspace doesn't want a filter bar. */}
      {!loading && !error && flows.length > 1 && (
        <div className="flow-toolbar">
          <div className="flow-search">
            <Search size={15} aria-hidden />
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t("flowList.searchPlaceholder")}
              aria-label={t("flowList.searchPlaceholder")}
            />
          </div>
          <label className="flow-filter">
            <span className="flow-filter-label">{t("flowList.filterStatus")}</span>
            <select
              value={statusFilter}
              onChange={(e) =>
                setStatusFilter(e.target.value as FlowRunStatus | "all")
              }
            >
              <option value="all">{t("flowList.statusAll")}</option>
              <option value="live">{t("flowStatus.live.label")}</option>
              <option value="manual">{t("flowStatus.manual.label")}</option>
              <option value="needs_publish">
                {t("flowStatus.needs_publish.label")}
              </option>
              <option value="paused">{t("flowStatus.paused.label")}</option>
            </select>
          </label>
          <label className="flow-filter">
            <span className="flow-filter-label">{t("flowList.sortBy")}</span>
            <select
              value={sort}
              onChange={(e) => setSort(e.target.value as SortKey)}
            >
              <option value="recent">{t("flowList.sortRecent")}</option>
              <option value="name">{t("flowList.sortName")}</option>
              <option value="status">{t("flowList.sortStatus")}</option>
            </select>
          </label>
        </div>
      )}
      {!loading && !error && flows.length > 0 && visibleFlows.length === 0 && (
        <div className="card flow-empty">
          <Search size={24} className="flow-empty-icon" />
          <p>{t("flowList.noMatches")}</p>
        </div>
      )}
      <div className="graph-list">
        {visibleFlows.map((f) => {
          const isPrivate = f.visibility === "private";
          const ownedByMe = !!me && f.owner === me.subject;
          const displayName = f.name || f.id;
          const schedEntries = scheduleByFlow.get(f.id);
          const sched =
            schedEntries && schedEntries.length
              ? summarizeFlowSchedule(schedEntries)
              : null;
          const runs = runsByFlow.get(f.id) ?? [];
          const lastRun = runs[0];
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
                  {/* Visibility badge is shown only for private flows; an
                      org-visible flow is the default, so an "Org" chip on
                      every other card was pure noise. Absence = shared. */}
                  {isPrivate && (
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
                  )}
                  {/* The card is a <Link>, so the delete action stops the
                      click from navigating and opens the password confirm.
                      Gated on graph:edit — viewers can browse but not delete;
                      the daemon is the final authority either way. */}
                  {canEdit && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="graph-card-duplicate"
                      title={t("flowList.duplicateFlow")}
                      aria-label={t("flowList.duplicateFlow")}
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        setDupTarget(f);
                      }}
                    >
                      <Copy size={14} />
                    </Button>
                  )}
                  {canEdit && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="graph-card-delete"
                      title={t("flowList.deleteFlow")}
                      aria-label={t("flowList.deleteFlow")}
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        setDeleteTarget(f);
                      }}
                    >
                      <Trash2 size={14} />
                    </Button>
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
                  <div className="graph-card-desc">{f.description}</div>
                )}
                {f.owner && !ownedByMe && (
                  <div className="meta" style={{ color: "var(--muted)" }}>
                    {t("flowList.createdBy", { owner: f.owner })}
                  </div>
                )}
                {/* Footer pinned to the card bottom so every card is the same
                    height regardless of which optional rows above are present —
                    the ragged grid was the loudest "unfinished" tell. */}
                <div className="graph-card-foot">
                  {lastRun ? (
                    <span
                      className="graph-card-lastrun"
                      title={formatDateTime(
                        lastRun.started_at || lastRun.enqueued_at,
                      )}
                    >
                      <span
                        className={`run-dot run-dot-${lastRun.status}`}
                        aria-hidden
                      />
                      {t("flowList.lastRun", {
                        when: formatRelative(
                          lastRun.started_at || lastRun.enqueued_at,
                          t,
                        ),
                      })}
                    </span>
                  ) : (
                    <span className="graph-card-lastrun is-empty">
                      {t("flowList.neverRun")}
                    </span>
                  )}
                  <RunSparkline runs={runs} />
                </div>
              </div>
            </Link>
          );
        })}
      </div>
      {deleteTarget && (
        <DeleteFlowModal
          flowName={deleteTarget.name || deleteTarget.id}
          onConfirm={(password) => deleteFlow(deleteTarget, password)}
          onClose={() => setDeleteTarget(null)}
        />
      )}
      {dupTarget && (
        <DuplicateFlowModal
          sourceName={dupTarget.name || dupTarget.id}
          defaultName={t("duplicateFlow.defaultName", {
            name: dupTarget.name || dupTarget.id,
          })}
          onConfirm={(name) => duplicateFlow(dupTarget, name)}
          onClose={() => setDupTarget(null)}
        />
      )}
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
  let statusText: string;
  let resume = false;
  let showToggle = false;
  if (sum.flowDisabled) {
    statusText = t("schedules.flowPaused");
  } else if (sum.active) {
    statusText = sum.nextRun
      ? t("flowList.nextRun", { when: formatNextRun(sum.nextRun, t) })
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
      <span className="flow-schedule-status">{statusText}</span>
      {showToggle && (
        <Button
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
        </Button>
      )}
    </div>
  );
}
