import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Activity, ExternalLink } from "lucide-react";
import { useTranslation } from "react-i18next";
import i18n from "../i18n";
import { useAuth } from "../auth";
import { api } from "../api";
import { shouldShowTenantID } from "../lib/visibleTenant";
import type { RunSummary, JobStatus } from "../types";

const PAGE_SIZE = 50;

export function RunList() {
  const { t } = useTranslation();
  // Status filter chips. Label keys (not literals) are resolved against
  // i18n at render time so the chips switch with the active locale.
  const STATUS_CHIPS: { labelKey: string; value: JobStatus | "" }[] = [
    { labelKey: "runList.filterAll", value: "" },
    { labelKey: "runList.filterRunning", value: "running" },
    { labelKey: "runList.filterAwaiting", value: "awaiting" },
    { labelKey: "runList.filterFailed", value: "failed" },
    { labelKey: "runList.filterSucceeded", value: "succeeded" },
  ];
  const { token, me, tenants, activeTenant, activeWorkspace } = useAuth();
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<JobStatus | "">("");
  const [hasMore, setHasMore] = useState(false);
  // graph_id → display name, so the FLOW column reads "Order received
  // alert" instead of the slug. Best-effort: a missing entry (deleted
  // flow, fetch error) falls back to the raw id.
  const [flowNames, setFlowNames] = useState<Record<string, string>>({});

  useEffect(() => {
    const tenant = activeTenant || me?.tenant || "";
    const workspace = activeWorkspace || me?.workspace || "";
    if (!token || !tenant || !workspace) return;
    let cancelled = false;
    api
      .listGraphs(token, tenant, workspace)
      .then((r) => {
        if (cancelled) return;
        const names: Record<string, string> = {};
        for (const g of r.graphs) if (g.name) names[g.id] = g.name;
        setFlowNames(names);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [token, activeTenant, activeWorkspace, me]);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    api
      .listAllRuns(token, {
        limit: PAGE_SIZE,
        status: filter || undefined,
        workspace: activeWorkspace || undefined,
        tenant: activeTenant || undefined,
      })
      .then((r) => {
        if (cancelled) return;
        const page = r.runs ?? [];
        setRuns(page);
        setHasMore(page.length === PAGE_SIZE);
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
  }, [token, filter, activeWorkspace, activeTenant]);

  // Live polling whenever anything is in-flight. Same heuristic as the
  // RunHistory dropdown — refresh only the first PAGE_SIZE rows so a
  // long scrollback isn't repeatedly fetched.
  useEffect(() => {
    if (!token) return;
    const anyLive = runs.some(
      (r) =>
        r.status === "queued" ||
        r.status === "running" ||
        r.status === "awaiting",
    );
    if (!anyLive) return;
    const t = window.setInterval(() => {
      api
        .listAllRuns(token, {
          limit: Math.max(PAGE_SIZE, runs.length),
          status: filter || undefined,
          workspace: activeWorkspace || undefined,
        })
        .then((r) => {
          const page = r.runs ?? [];
          setRuns(page);
          setHasMore(page.length >= PAGE_SIZE);
        })
        .catch(() => {});
    }, 3000);
    return () => window.clearInterval(t);
  }, [token, runs, filter, activeWorkspace, activeTenant]);

  const loadMore = async () => {
    if (!token || loading) return;
    setLoading(true);
    try {
      const r = await api.listAllRuns(token, {
        limit: PAGE_SIZE,
        offset: runs.length,
        status: filter || undefined,
        workspace: activeWorkspace || undefined,
        tenant: activeTenant || undefined,
      });
      const next = r.runs ?? [];
      setRuns((prev) => [...prev, ...next]);
      setHasMore(next.length === PAGE_SIZE);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("runList.title")}</h1>
          <div className="sub">
            {t(
              shouldShowTenantID(me, tenants.length)
                ? "runList.subtitle"
                : "runList.subtitleWorkspaceOnly",
              {
                tenant: activeTenant || me?.tenant,
                workspace:
                  activeWorkspace || me?.workspace || t("runList.anyWorkspace"),
              },
            )}
          </div>
        </div>
      </div>

      <div className="run-history-filters" style={{ marginBottom: "var(--space-4)" }}>
        {STATUS_CHIPS.map((c) => (
          <button
            key={c.labelKey}
            type="button"
            className={
              "run-filter-chip" + (filter === c.value ? " active" : "")
            }
            onClick={() => setFilter(c.value)}
          >
            {t(c.labelKey)}
          </button>
        ))}
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)" }}>
          {error}
        </div>
      )}
      {!error && loading && runs.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      )}
      {!error && !loading && runs.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("runList.empty")}
        </div>
      )}

      {runs.length > 0 && (
        <div className="card" style={{ padding: 0, overflow: "hidden" }}>
          <table className="run-table">
            <thead>
              <tr>
                <th style={{ width: 28 }}></th>
                <th>{t("runList.colRun")}</th>
                <th>{t("runList.colFlow")}</th>
                <th>{t("runList.colStarted")}</th>
                <th>{t("runList.colDuration")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {runs.map((r) => (
                <tr key={r.id}>
                  <td>
                    <span className={"status-dot " + r.status} />
                  </td>
                  <td style={{ fontFamily: "var(--font-mono)", fontSize: 12 }}>
                    {/* Primary action: open the run-detail page, the
                        "what happened" surface (T2). The graph-name
                        link below still goes to the editor for
                        "make changes" flows. */}
                    <Link
                      to={`/runs/${encodeURIComponent(r.id)}`}
                      style={{ textDecoration: "none" }}
                    >
                      {r.id.slice(0, 12)}
                    </Link>
                  </td>
                  <td>
                    <Link
                      to={`/flows/${encodeURIComponent(r.graph_id)}?run=${encodeURIComponent(r.id)}`}
                      style={{
                        display: "inline-flex",
                        alignItems: "center",
                        gap: 4,
                      }}
                    >
                      <Activity size={12} />
                      {flowNames[r.graph_id] ?? r.graph_id}
                    </Link>
                  </td>
                  <td style={{ color: "var(--muted)", fontSize: 12 }}>
                    {formatTime(r.enqueued_at)}
                  </td>
                  <td style={{ color: "var(--muted)", fontSize: 12 }}>
                    {/* Older records (pre started_at-stamping) fall back
                        to enqueued_at so finished runs still show a
                        duration instead of "—". */}
                    {r.finished_at && (r.started_at || r.enqueued_at)
                      ? formatDuration(r.started_at ?? r.enqueued_at, r.finished_at)
                      : r.status === "running"
                      ? t("runList.inProgress")
                      : "—"}
                    {r.error_code && (
                      <span style={{ color: "var(--danger)", marginLeft: 6 }}>
                        · {r.error_code}
                      </span>
                    )}
                  </td>
                  <td style={{ textAlign: "right", paddingRight: 12 }}>
                    <Link
                      to={`/runs/${encodeURIComponent(r.id)}`}
                      style={{ color: "var(--muted)" }}
                      title={t("runList.openDetails")}
                    >
                      <ExternalLink size={14} />
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {hasMore && (
        <div style={{ textAlign: "center", marginTop: "var(--space-4)" }}>
          <button onClick={loadMore} disabled={loading}>
            {loading ? t("common.loading") : t("runList.loadMore")}
          </button>
        </div>
      )}
    </div>
  );
}

// formatTime renders a relative time string ("3m ago", "2h ago", …).
// Pulls the active locale via the singleton i18n instance — avoids
// threading `t` through table-row helpers.
function formatTime(iso: string): string {
  const ts = Date.parse(iso);
  if (!Number.isFinite(ts)) return iso;
  const diffSec = Math.max(0, Math.round((Date.now() - ts) / 1000));
  if (diffSec < 60) return i18n.t("runList.secondsAgo", { count: diffSec });
  if (diffSec < 3600) return i18n.t("runList.minutesAgo", { count: Math.round(diffSec / 60) });
  if (diffSec < 86400) return i18n.t("runList.hoursAgo", { count: Math.round(diffSec / 3600) });
  return i18n.t("runList.daysAgo", { count: Math.round(diffSec / 86400) });
}

function formatDuration(startedISO: string, finishedISO: string): string {
  const start = Date.parse(startedISO);
  const end = Date.parse(finishedISO);
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "";
  const ms = Math.max(0, end - start);
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.round(ms / 60_000)}m`;
}
