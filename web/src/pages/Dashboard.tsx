// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Activity,
  CheckCircle2,
  AlertTriangle,
  Inbox,
  Workflow,
  Plus,
  ArrowRight,
  Share2,
} from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import { formatRelative, formatDateTime } from "../lib/datetime";
import { ShareOverviewModal } from "../components/ShareOverviewModal";
import { Button } from "../components/Button";
import type { FlowSummary, PendingApproval, RunSummary } from "../types";

// Dashboard is the workspace overview — the "is everything healthy?" landing
// pro automation tools open to. It answers four questions at a glance (runs
// today, success rate, failures needing attention, approvals waiting), then
// lists the failed runs to act on and the most recent activity. All derived
// client-side from three cheap list calls; no new backend. Private and
// unpublished (needs_publish) flows — and their runs — are excluded so
// owner-scoped test-mode activity doesn't skew the workspace health numbers.
const RUN_WINDOW = 200; // recent runs to summarize
const ATTENTION_MAX = 5;
const RECENT_MAX = 8;

function startOfToday(): number {
  const d = new Date();
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

export function Dashboard() {
  const { t } = useTranslation();
  const { token, me, activeTenant, activeWorkspace } = useAuth();
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [flows, setFlows] = useState<FlowSummary[]>([]);
  const [approvals, setApprovals] = useState<PendingApproval[]>([]);
  const [loading, setLoading] = useState(true);
  const [shareOpen, setShareOpen] = useState(false);

  useEffect(() => {
    if (!token || !activeWorkspace) return;
    let cancelled = false;
    setLoading(true);
    const tenant = activeTenant || undefined;
    const workspace = activeWorkspace || undefined;
    Promise.allSettled([
      api.listAllRuns(token, { limit: RUN_WINDOW, workspace, tenant }),
      api.listGraphs(token, activeTenant, activeWorkspace),
      api.listPendingApprovals(token, { workspace, tenant }),
    ]).then(([r, g, a]) => {
      if (cancelled) return;
      if (r.status === "fulfilled") setRuns(r.value.runs ?? []);
      if (g.status === "fulfilled") setFlows(g.value.graphs ?? []);
      if (a.status === "fulfilled") setApprovals(a.value.approvals ?? []);
      setLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, [token, activeTenant, activeWorkspace]);

  // Only "real" flows count toward the overview. Private flows are scoped to
  // a single owner (the public board hides them too), and needs_publish flows
  // are configured-but-unpublished drafts the scheduler won't run yet — both
  // are effectively in test mode, so their runs would skew the health stats.
  // We exclude both the flows and any run belonging to them.
  const countedFlows = useMemo(
    () =>
      flows.filter(
        (f) => f.visibility !== "private" && f.run_status !== "needs_publish",
      ),
    [flows],
  );
  const countedRuns = useMemo(() => {
    const ids = new Set(countedFlows.map((f) => f.id));
    return runs.filter((r) => ids.has(r.graph_id));
  }, [runs, countedFlows]);

  const stats = useMemo(() => {
    const today = startOfToday();
    const runsToday = countedRuns.filter((r) => {
      const ts = Date.parse(r.started_at || r.enqueued_at || "");
      return Number.isFinite(ts) && ts >= today;
    }).length;
    const finished = countedRuns.filter(
      (r) => r.status === "succeeded" || r.status === "failed",
    );
    const succeeded = finished.filter((r) => r.status === "succeeded").length;
    const successRate = finished.length
      ? Math.round((succeeded / finished.length) * 100)
      : null;
    const liveFlows = countedFlows.filter((f) => f.run_status === "live").length;
    return { runsToday, successRate, finishedCount: finished.length, liveFlows };
  }, [countedRuns, countedFlows]);

  // A flow needs attention when its MOST RECENT run failed — not every
  // historical failure. Runs arrive newest-first (ORDER BY enqueued_at DESC),
  // so the first run we see for a graph is its latest; once that flow runs
  // again — succeeds, or is back to running/pending — it drops off this list.
  // One entry per flow so a flaky flow doesn't flood the count. Uncapped: the
  // stat card shows the true total (and must match the public board, which
  // counts the same way); the panel below renders only the first ATTENTION_MAX.
  const attentionRuns = useMemo(() => {
    const seen = new Set<string>();
    const out: RunSummary[] = [];
    for (const r of countedRuns) {
      if (seen.has(r.graph_id)) continue; // already saw this flow's latest run
      seen.add(r.graph_id);
      if (r.status === "failed") out.push(r);
    }
    return out;
  }, [countedRuns]);
  const recentRuns = useMemo(
    () => countedRuns.slice(0, RECENT_MAX),
    [countedRuns],
  );
  const flowName = (id: string) =>
    flows.find((f) => f.id === id)?.name || id;

  const greeting = me?.subject ? me.subject.split("@")[0] : "";

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("dashboard.title")}</h1>
          <div className="sub">
            {greeting
              ? t("dashboard.greeting", { name: greeting })
              : t("dashboard.subtitle")}
          </div>
        </div>
        <div className="dash-title-actions">
          <Button
            className="dash-share"
            onClick={() => setShareOpen(true)}
            title={t("share.title")}
          >
            <Share2 size={16} style={{ marginRight: 6 }} />
            {t("share.action")}
          </Button>
          <Link to="/flows/new" className="btn primary dash-new">
            <Plus size={16} style={{ marginRight: 6 }} />
            {t("flowList.newFlow")}
          </Link>
        </div>
      </div>

      {shareOpen && <ShareOverviewModal onClose={() => setShareOpen(false)} />}

      <div className="dash-stats">
        <StatCard
          icon={<Activity size={18} />}
          label={t("dashboard.runsToday")}
          value={loading ? "—" : String(stats.runsToday)}
          to="/runs"
        />
        <StatCard
          icon={<CheckCircle2 size={18} />}
          label={t("dashboard.successRate")}
          value={
            loading || stats.successRate === null
              ? "—"
              : `${stats.successRate}%`
          }
          sub={
            stats.finishedCount
              ? t("dashboard.overRuns", { count: stats.finishedCount })
              : undefined
          }
          tone={
            stats.successRate !== null && stats.successRate < 80
              ? "warn"
              : "good"
          }
          to="/runs"
        />
        <StatCard
          icon={<AlertTriangle size={18} />}
          label={t("dashboard.needsAttention")}
          value={loading ? "—" : String(attentionRuns.length)}
          tone={attentionRuns.length > 0 ? "bad" : "good"}
          to="/runs?status=failed"
        />
        <StatCard
          icon={<Inbox size={18} />}
          label={t("dashboard.approvalsWaiting")}
          value={loading ? "—" : String(approvals.length)}
          tone={approvals.length > 0 ? "warn" : "good"}
          to="/approvals"
        />
      </div>

      <div className="dash-grid">
        {/* Failures to act on — the operator's first stop after "is anything
            broken?". Empty state reads as reassurance, not a void. */}
        <section className="card dash-panel">
          <div className="dash-panel-head">
            <h2>{t("dashboard.needsAttention")}</h2>
            {attentionRuns.length > 0 && (
              <Link to="/runs?status=failed" className="dash-panel-link">
                {t("dashboard.viewAll")} <ArrowRight size={13} />
              </Link>
            )}
          </div>
          {loading ? (
            <p className="dash-empty">{t("common.loading")}</p>
          ) : attentionRuns.length === 0 ? (
            <p className="dash-empty dash-empty-good">
              <CheckCircle2 size={16} />
              {t("dashboard.noFailures")}
            </p>
          ) : (
            <ul className="dash-runlist">
              {attentionRuns.slice(0, ATTENTION_MAX).map((r) => (
                <li key={r.id}>
                  <Link to={`/runs/${encodeURIComponent(r.id)}`}>
                    <span className="status-dot failed" />
                    <span className="dash-run-flow">{flowName(r.graph_id)}</span>
                    {/* The raw error_code is a technical token; on the
                        overview the failed dot + flow name say enough, and the
                        run detail explains the cause in plain language. */}
                    <span
                      className="dash-run-time"
                      title={formatDateTime(r.started_at || r.enqueued_at)}
                    >
                      {formatRelative(r.started_at || r.enqueued_at, t)}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </section>

        {/* Recent activity — the live pulse of the workspace. */}
        <section className="card dash-panel">
          <div className="dash-panel-head">
            <h2>{t("dashboard.recentActivity")}</h2>
            <Link to="/runs" className="dash-panel-link">
              {t("dashboard.viewAll")} <ArrowRight size={13} />
            </Link>
          </div>
          {loading ? (
            <p className="dash-empty">{t("common.loading")}</p>
          ) : recentRuns.length === 0 ? (
            <p className="dash-empty">{t("dashboard.noRuns")}</p>
          ) : (
            <ul className="dash-runlist">
              {recentRuns.map((r) => (
                <li key={r.id}>
                  <Link to={`/runs/${encodeURIComponent(r.id)}`}>
                    <span className={"status-dot " + r.status} />
                    <span className="dash-run-flow">{flowName(r.graph_id)}</span>
                    <span
                      className="dash-run-time"
                      title={formatDateTime(r.started_at || r.enqueued_at)}
                    >
                      {formatRelative(r.started_at || r.enqueued_at, t)}
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      {/* Footer line: flow inventory, with a quick jump to the full list. */}
      <div className="dash-foot">
        <Workflow size={15} />
        {t("dashboard.flowSummary", {
          total: countedFlows.length,
          live: stats.liveFlows,
        })}
        <Link to="/flows" className="dash-panel-link">
          {t("dashboard.manageFlows")} <ArrowRight size={13} />
        </Link>
      </div>
    </div>
  );
}

function StatCard({
  icon,
  label,
  value,
  sub,
  tone = "neutral",
  to,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  sub?: string;
  tone?: "neutral" | "good" | "warn" | "bad";
  to: string;
}) {
  return (
    <Link to={to} className={"card dash-stat dash-stat-" + tone}>
      <span className="dash-stat-icon">{icon}</span>
      <span className="dash-stat-value">{value}</span>
      <span className="dash-stat-label">{label}</span>
      {sub && <span className="dash-stat-sub">{sub}</span>}
    </Link>
  );
}
