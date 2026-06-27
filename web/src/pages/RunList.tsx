// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Activity, ExternalLink, RotateCcw, Search } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";
import { shouldShowTenantID } from "../lib/visibleTenant";
import { explainApiError } from "../lib/explainApiError";
import { formatDateTime } from "../lib/datetime";
import type { RunSummary, JobStatus } from "../types";

const PAGE_SIZE = 50;

// runStatusLabel maps a run's machine status to a human label so the status
// column carries meaning beyond the color dot (accessibility + clarity).
function runStatusLabel(status: JobStatus, t: (key: string) => string): string {
  switch (status) {
    case "queued":
      return t("runList.statusQueued");
    case "running":
      return t("runList.statusRunning");
    case "awaiting":
      return t("runList.statusAwaiting");
    case "succeeded":
      return t("runList.statusSucceeded");
    case "failed":
      return t("runList.statusFailed");
    case "cancelled":
      return t("runList.statusCancelled");
    default:
      return status;
  }
}

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
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Seed the status filter from a ?status= query param so deep links (e.g. the
  // dashboard's "Needs attention" card → ?status=failed) land pre-filtered.
  // Only a value matching one of the chips is honoured; anything else = all.
  const [filter, setFilter] = useState<JobStatus | "">(() => {
    const s = searchParams.get("status");
    return STATUS_CHIPS.some((c) => c.value === s) ? (s as JobStatus) : "";
  });
  // Free-text filter over the loaded rows (run id + flow name). Client-side
  // by design: the runs API has no text-search param, so this narrows what's
  // already fetched rather than querying the server.
  const [query, setQuery] = useState("");
  // Per-flow filter. "" = all flows (listAllRuns); a graph_id switches the
  // fetch to that flow's own run history (listRuns), so it's server-side and
  // paginates correctly past the first page.
  const [flowFilter, setFlowFilter] = useState("");
  const [hasMore, setHasMore] = useState(false);
  // Failed-runs inbox: ids the user has checked for bulk retry. Only
  // populated/shown in the Failed filter, where retrying makes sense.
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [retrying, setRetrying] = useState(false);
  // Confirm before a bulk retry — it resumes many runs at once (re-running
  // their failed-and-downstream steps, side effects included).
  const [confirmBulk, setConfirmBulk] = useState(false);
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

  // One fetch path for every load site (initial, poll, load-more, post-retry
  // refresh) so the all-flows vs per-flow branch lives in a single place.
  // Per-flow uses listRuns (that flow's own paginated history); all-flows uses
  // listAllRuns. Both honour the status filter.
  const fetchRunsPage = useCallback(
    (offset: number, limit: number = PAGE_SIZE) => {
      const tok = token!;
      if (flowFilter) {
        return api
          .listRuns(
            tok,
            activeTenant || me?.tenant || "",
            activeWorkspace || me?.workspace || "",
            flowFilter,
            { limit, offset, status: filter || undefined },
          )
          .then((r) => r.runs ?? []);
      }
      return api
        .listAllRuns(tok, {
          limit,
          offset,
          status: filter || undefined,
          workspace: activeWorkspace || undefined,
          tenant: activeTenant || undefined,
        })
        .then((r) => r.runs ?? []);
    },
    [token, flowFilter, filter, activeTenant, activeWorkspace, me],
  );

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    setSelected(new Set());
    fetchRunsPage(0)
      .then((page) => {
        if (cancelled) return;
        setRuns(page);
        setHasMore(page.length === PAGE_SIZE);
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
  }, [token, fetchRunsPage, t]);

  // Live polling whenever anything is in-flight — refresh only the first
  // PAGE_SIZE rows so a long scrollback isn't repeatedly fetched.
  //
  // The interval depends on the derived `anyLive` boolean, NOT the whole
  // `runs` array — the tick calls setRuns, so depending on `runs` rebuilt
  // the interval every 3s (a teardown + new timer per tick). The current
  // row count (for the refresh limit) is read from a ref so the callback
  // stays stable.
  const anyLive = runs.some(
    (r) =>
      r.status === "queued" ||
      r.status === "running" ||
      r.status === "awaiting",
  );
  const runCountRef = useRef(runs.length);
  runCountRef.current = runs.length;
  useEffect(() => {
    if (!token || !anyLive) return;
    const t = window.setInterval(() => {
      fetchRunsPage(0, Math.max(PAGE_SIZE, runCountRef.current))
        .then((page) => {
          setRuns(page);
          setHasMore(page.length >= PAGE_SIZE);
        })
        .catch(() => {});
    }, 3000);
    return () => window.clearInterval(t);
  }, [token, anyLive, fetchRunsPage]);

  // The Failed filter doubles as a retry inbox: checkboxes + a bulk
  // "Retry selected" that resumes each failed run from where it failed.
  const showInbox = filter === "failed";

  const toggleSelected = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const toggleSelectAll = () =>
    setSelected((prev) =>
      prev.size === visibleRuns.length
        ? new Set()
        : new Set(visibleRuns.map((r) => r.id)),
    );

  const retryOne = async (id: string) => {
    if (!token) return;
    setRetrying(true);
    setError(null);
    try {
      const { job_id } = await api.retryRun(token, id);
      navigate(`/runs/${encodeURIComponent(job_id)}`);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setRetrying(false);
    }
  };

  const bulkRetry = async () => {
    if (!token || selected.size === 0) return;
    setRetrying(true);
    setError(null);
    const ids = [...selected];
    try {
      const results = await Promise.allSettled(ids.map((id) => api.retryRun(token, id)));
      const failures = results.filter((r) => r.status === "rejected").length;
      setSelected(new Set());
      if (failures > 0) {
        setError(t("runList.bulkRetryPartial", { failed: failures, total: ids.length }));
      }
      // Refresh so the new runs appear and the retried ones update.
      setRuns(await fetchRunsPage(0));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setRetrying(false);
    }
  };

  const loadMore = async () => {
    if (!token || loading) return;
    setLoading(true);
    try {
      const next = await fetchRunsPage(runs.length);
      setRuns((prev) => [...prev, ...next]);
      setHasMore(next.length === PAGE_SIZE);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  };

  // Flow dropdown options, sorted by display name — only flows that have a
  // name resolved (others stay reachable via "All flows").
  const flowOptions = useMemo(
    () =>
      Object.entries(flowNames).sort((a, b) => a[1].localeCompare(b[1])),
    [flowNames],
  );

  // Text filter applied to the loaded rows: matches run id or flow name.
  const visibleRuns = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return runs;
    return runs.filter(
      (r) =>
        r.id.toLowerCase().includes(q) ||
        (flowNames[r.graph_id] ?? r.graph_id).toLowerCase().includes(q),
    );
  }, [runs, query, flowNames]);

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("runList.title")}</h1>
          <div className="sub">
            {shouldShowTenantID(me, tenants.length)
              ? t("runList.subtitle", { tenant: activeTenant || me?.tenant })
              : t("runList.subtitleWorkspaceOnly")}
          </div>
        </div>
      </div>

      <div className="run-history-filters" style={{ marginBottom: "var(--space-3)" }}>
        {STATUS_CHIPS.map((c) => (
          <Button
            key={c.labelKey}
            className={
              "run-filter-chip" + (filter === c.value ? " active" : "")
            }
            onClick={() => setFilter(c.value)}
          >
            {t(c.labelKey)}
          </Button>
        ))}
      </div>

      <div className="run-toolbar">
        <div className="flow-search">
          <Search size={15} aria-hidden />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("runList.searchPlaceholder")}
            aria-label={t("runList.searchPlaceholder")}
          />
        </div>
        <label className="flow-filter">
          <span className="flow-filter-label">{t("runList.filterFlow")}</span>
          <select
            value={flowFilter}
            onChange={(e) => setFlowFilter(e.target.value)}
          >
            <option value="">{t("runList.allFlows")}</option>
            {flowOptions.map(([id, name]) => (
              <option key={id} value={id}>
                {name}
              </option>
            ))}
          </select>
        </label>
        <span className="run-count">
          {t("runList.resultCount", {
            count: visibleRuns.length,
            more: hasMore ? "+" : "",
          })}
        </span>
      </div>

      {/* Failed-runs inbox bulk action. Appears in the Failed filter once
          one or more runs are checked: retry resumes each from where it
          failed (reusing the work that already succeeded). */}
      {showInbox && selected.size > 0 && (
        <div
          className="card"
          style={{
            display: "flex",
            alignItems: "center",
            gap: "var(--space-3)",
            marginBottom: "var(--space-4)",
            padding: "var(--space-2) var(--space-3)",
          }}
        >
          <span>{t("runList.selectedCount", { count: selected.size })}</span>
          <Button
            variant="primary"
            onClick={() => setConfirmBulk(true)}
            disabled={retrying}
            style={{ display: "inline-flex", alignItems: "center", gap: 4 }}
          >
            <RotateCcw size={14} />
            {retrying ? t("runList.retrying") : t("runList.retrySelected")}
          </Button>
          <Button variant="ghost" onClick={() => setSelected(new Set())} disabled={retrying}>
            {t("runList.clearSelection")}
          </Button>
        </div>
      )}

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

      {visibleRuns.length > 0 && (
        <div className="card" style={{ padding: 0, overflow: "hidden" }}>
          {/* Horizontal-scroll wrapper: on a narrow screen the table keeps a
              readable minimum width and scrolls inside the card rather than
              overflowing the viewport. The card's overflow:hidden still clips
              it to the rounded corners. */}
          <div className="run-table-scroll">
          <table className="run-table">
            <thead>
              <tr>
                {showInbox && (
                  <th style={{ width: 28 }}>
                    <input
                      type="checkbox"
                      aria-label={t("runList.selectAll")}
                      checked={
                        visibleRuns.length > 0 &&
                        selected.size === visibleRuns.length
                      }
                      ref={(el) => {
                        if (el)
                          el.indeterminate =
                            selected.size > 0 &&
                            selected.size < visibleRuns.length;
                      }}
                      onChange={toggleSelectAll}
                    />
                  </th>
                )}
                <th style={{ width: 28 }}></th>
                <th>{t("runList.colFlow")}</th>
                <th>{t("runList.colStarted")}</th>
                <th>{t("runList.colDuration")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {visibleRuns.map((r) => (
                <tr key={r.id}>
                  {showInbox && (
                    <td>
                      <input
                        type="checkbox"
                        aria-label={t("runList.selectRun", { id: r.id.slice(0, 8) })}
                        checked={selected.has(r.id)}
                        onChange={() => toggleSelected(r.id)}
                      />
                    </td>
                  )}
                  <td>
                    {/* Colored dot only — the green/red outcome reads at a
                        glance and the status word beside it was redundant.
                        The label is kept on aria-label + title so the meaning
                        stays available to screen readers and on hover (so a
                        color-blind user can still disambiguate — notably
                        "Waiting for approval", which parks on the viewer). */}
                    <span
                      className={"status-dot " + r.status}
                      role="img"
                      aria-label={runStatusLabel(r.status, t)}
                      title={runStatusLabel(r.status, t)}
                    />
                  </td>
                  <td>
                    {/* Flow name is the primary identifier — it's how a user
                        thinks about a run ("the order-alert flow"), not the
                        opaque id. The bold name links to the editor ("make
                        changes"); the muted run id beneath links to the
                        run-detail "what happened" surface (T2), so both
                        destinations stay reachable. */}
                    <Link
                      to={`/flows/${encodeURIComponent(r.graph_id)}?run=${encodeURIComponent(r.id)}`}
                      style={{
                        display: "inline-flex",
                        alignItems: "center",
                        gap: 4,
                        fontWeight: 600,
                      }}
                    >
                      <Activity size={12} />
                      {flowNames[r.graph_id] ?? r.graph_id}
                    </Link>
                    <Link
                      to={`/runs/${encodeURIComponent(r.id)}`}
                      style={{
                        display: "block",
                        marginTop: 2,
                        fontFamily: "var(--font-mono)",
                        fontSize: "var(--text-xs)",
                        color: "var(--muted)",
                        textDecoration: "none",
                      }}
                    >
                      {r.id.slice(0, 12)}
                    </Link>
                  </td>
                  <td style={{ color: "var(--muted)", fontSize: "var(--text-sm)" }}>
                    {formatTime(r.enqueued_at)}
                  </td>
                  <td style={{ color: "var(--muted)", fontSize: "var(--text-sm)" }}>
                    {/* Older records (pre started_at-stamping) fall back
                        to enqueued_at so finished runs still show a
                        duration instead of "—". */}
                    {r.finished_at && (r.started_at || r.enqueued_at)
                      ? formatDuration(r.started_at ?? r.enqueued_at, r.finished_at)
                      : r.status === "running"
                      ? t("runList.inProgress")
                      : r.status === "awaiting"
                      ? /* not finished — parked on the viewer; "—" would read
                           as "done with no duration" */ t("runList.statusAwaiting")
                      : "—"}
                    {/* The raw error_code (e.g. "egress_blocked") is a
                        technical token that means nothing to most users — the
                        failed status is already shown, and opening the run
                        gives a plain-English explanation. So don't surface the
                        code here. */}
                  </td>
                  <td style={{ textAlign: "right", paddingRight: 12 }}>
                    {showInbox && (
                      <Button
                        className="btn-ghost"
                        onClick={() => void retryOne(r.id)}
                        disabled={retrying}
                        title={t("runList.retryRun")}
                        style={{ display: "inline-flex", alignItems: "center", gap: 4, marginRight: 8 }}
                      >
                        <RotateCcw size={13} />
                        {t("runList.retry")}
                      </Button>
                    )}
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
        </div>
      )}

      {runs.length > 0 && visibleRuns.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("runList.noMatches")}
        </div>
      )}

      {hasMore && (
        <div style={{ textAlign: "center", marginTop: "var(--space-4)" }}>
          <Button onClick={loadMore} disabled={loading}>
            {loading ? t("common.loading") : t("runList.loadMore")}
          </Button>
        </div>
      )}

      {confirmBulk && (
        <ConfirmModal
          title={t("runList.confirmBulkRetryTitle")}
          message={t("runList.confirmBulkRetryBody", { count: selected.size })}
          confirmLabel={t("runList.retrySelected")}
          danger
          onConfirm={() => {
            setConfirmBulk(false);
            void bulkRetry();
          }}
          onCancel={() => setConfirmBulk(false)}
        />
      )}
    </div>
  );
}

// formatTime renders a relative time string ("3m ago", "2h ago", …).
// Pulls the active locale via the singleton i18n instance — avoids
// threading `t` through table-row helpers.
// Standard local "YYYY-MM-DD HH:MM" everywhere — no relative "ago" strings.
function formatTime(iso: string): string {
  return formatDateTime(iso);
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
