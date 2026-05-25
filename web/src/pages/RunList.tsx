import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Activity, ExternalLink } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import type { RunSummary, JobStatus } from "../types";

// RunList shows every run across every graph in the principal's
// workspace. Filterable by status, paginated with a Load-more button,
// and live-polls the first page when something is mid-flight. Each row
// links to the editor for that graph with the run pre-selected so the
// canvas pre-fills with that run's node statuses + output preview.
const STATUS_CHIPS: { label: string; value: JobStatus | "" }[] = [
  { label: "All", value: "" },
  { label: "Running", value: "running" },
  { label: "Awaiting", value: "awaiting" },
  { label: "Failed", value: "failed" },
  { label: "Succeeded", value: "succeeded" },
];

const PAGE_SIZE = 50;

export function RunList() {
  const { token, me, activeTenant, activeWorkspace } = useAuth();
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<JobStatus | "">("");
  const [hasMore, setHasMore] = useState(false);

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
          <h1>Runs</h1>
          <div className="sub">
            All runs in {activeTenant || me?.tenant}/
            {activeWorkspace || me?.workspace || "(any)"}
          </div>
        </div>
      </div>

      <div className="run-history-filters" style={{ marginBottom: "var(--space-4)" }}>
        {STATUS_CHIPS.map((c) => (
          <button
            key={c.label}
            type="button"
            className={
              "run-filter-chip" + (filter === c.value ? " active" : "")
            }
            onClick={() => setFilter(c.value)}
          >
            {c.label}
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
          Loading…
        </div>
      )}
      {!error && !loading && runs.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          No runs match this filter.
        </div>
      )}

      {runs.length > 0 && (
        <div className="card" style={{ padding: 0, overflow: "hidden" }}>
          <table className="run-table">
            <thead>
              <tr>
                <th style={{ width: 28 }}></th>
                <th>Run</th>
                <th>Flow</th>
                <th>Started</th>
                <th>Duration</th>
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
                    {r.id.slice(0, 12)}
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
                      {r.graph_id}
                    </Link>
                  </td>
                  <td style={{ color: "var(--muted)", fontSize: 12 }}>
                    {formatTime(r.enqueued_at)}
                  </td>
                  <td style={{ color: "var(--muted)", fontSize: 12 }}>
                    {r.started_at && r.finished_at
                      ? formatDuration(r.started_at, r.finished_at)
                      : r.status === "running"
                      ? "in progress"
                      : "—"}
                    {r.error_code && (
                      <span style={{ color: "var(--danger)", marginLeft: 6 }}>
                        · {r.error_code}
                      </span>
                    )}
                  </td>
                  <td style={{ textAlign: "right", paddingRight: 12 }}>
                    <Link
                      to={`/flows/${encodeURIComponent(r.graph_id)}?run=${encodeURIComponent(r.id)}`}
                      style={{ color: "var(--muted)" }}
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
            {loading ? "Loading…" : "Load more"}
          </button>
        </div>
      )}
    </div>
  );
}

function formatTime(iso: string): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return iso;
  const diffSec = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (diffSec < 60) return `${diffSec}s ago`;
  if (diffSec < 3600) return `${Math.round(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.round(diffSec / 3600)}h ago`;
  return `${Math.round(diffSec / 86400)}d ago`;
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
