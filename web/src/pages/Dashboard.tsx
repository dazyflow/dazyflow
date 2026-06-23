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
} from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import { formatRelative, formatDateTime } from "../lib/datetime";
import type { FlowSummary, PendingApproval, RunSummary } from "../types";

// Dashboard is the workspace overview — the "is everything healthy?" landing
// pro automation tools open to. It answers four questions at a glance (runs
// today, success rate, failures needing attention, approvals waiting), then
// lists the failed runs to act on and the most recent activity. All derived
// client-side from three cheap list calls; no new backend.
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

  const stats = useMemo(() => {
    const today = startOfToday();
    const runsToday = runs.filter((r) => {
      const ts = Date.parse(r.started_at || r.enqueued_at || "");
      return Number.isFinite(ts) && ts >= today;
    }).length;
    const finished = runs.filter(
      (r) => r.status === "succeeded" || r.status === "failed",
    );
    const succeeded = finished.filter((r) => r.status === "succeeded").length;
    const successRate = finished.length
      ? Math.round((succeeded / finished.length) * 100)
      : null;
    const liveFlows = flows.filter((f) => f.run_status === "live").length;
    return { runsToday, successRate, finishedCount: finished.length, liveFlows };
  }, [runs, flows]);

  const failedRuns = useMemo(
    () => runs.filter((r) => r.status === "failed").slice(0, ATTENTION_MAX),
    [runs],
  );
  const recentRuns = useMemo(() => runs.slice(0, RECENT_MAX), [runs]);
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
        <Link to="/flows/new" className="primary dash-new">
          <Plus size={16} style={{ marginRight: 6 }} />
          {t("flowList.newFlow")}
        </Link>
      </div>

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
          value={loading ? "—" : String(failedRuns.length)}
          tone={failedRuns.length > 0 ? "bad" : "good"}
          to="/runs"
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
            {failedRuns.length > 0 && (
              <Link to="/runs" className="dash-panel-link">
                {t("dashboard.viewAll")} <ArrowRight size={13} />
              </Link>
            )}
          </div>
          {loading ? (
            <p className="dash-empty">{t("common.loading")}</p>
          ) : failedRuns.length === 0 ? (
            <p className="dash-empty dash-empty-good">
              <CheckCircle2 size={16} />
              {t("dashboard.noFailures")}
            </p>
          ) : (
            <ul className="dash-runlist">
              {failedRuns.map((r) => (
                <li key={r.id}>
                  <Link to={`/runs/${encodeURIComponent(r.id)}`}>
                    <span className="status-dot failed" />
                    <span className="dash-run-flow">{flowName(r.graph_id)}</span>
                    {r.error_code && (
                      <span className="dash-run-error">{r.error_code}</span>
                    )}
                    <span
                      className="dash-run-time"
                      title={formatDateTime(r.started_at || r.enqueued_at)}
                    >
                      {formatRelative(r.started_at || r.enqueued_at)}
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
                      {formatRelative(r.started_at || r.enqueued_at)}
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
          total: flows.length,
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
