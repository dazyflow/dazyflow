// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Activity, Pencil, RotateCcw, Search } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api } from "../../api";
import { Button } from "../../components/ui/Button";
import { ConfirmModal } from "../../components/ui/ConfirmModal";
import { explainApiError } from "../../lib/explainApiError";
import { formatDateTime } from "../../lib/datetime";
import type { RunSummary, JobStatus } from "../../types";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { POLL } from "../../lib/timing";
import { formatDuration } from "../../lib/format";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";

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
  const { token, me, activeTenant, activeWorkspace } = useAuth();
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
  // Date-range filter over a run's enqueue time. Both are "YYYY-MM-DD" from
  // <input type="date">; they're resolved to local-midnight ISO instants
  // before the fetch (see dayStartISO/dayEndExclusiveISO). Server-side and
  // paginated — unlike the text `query`, which only narrows loaded rows.
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
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
      const sinceISO = dayStartISO(since);
      const untilISO = dayEndExclusiveISO(until);
      if (flowFilter) {
        return api
          .listRuns(
            tok,
            activeTenant || me?.tenant || "",
            activeWorkspace || me?.workspace || "",
            flowFilter,
            { limit, offset, status: filter || undefined, since: sinceISO, until: untilISO },
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
          since: sinceISO,
          until: untilISO,
        })
        .then((r) => r.runs ?? []);
    },
    [token, flowFilter, filter, since, until, activeTenant, activeWorkspace, me],
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
  // the interval on every tick (a teardown + new timer per tick). The current
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
    }, POLL.live);
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
        (flowNames[r.graph_id] ?? "").toLowerCase().includes(q),
    );
  }, [runs, query, flowNames]);

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("runList.title")}</h1>
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
          <Search size={ICON.sm} aria-hidden />
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
        <label className="flow-filter">
          <span className="flow-filter-label">{t("runList.filterFrom")}</span>
          <input
            type="date"
            value={since}
            max={until || undefined}
            onChange={(e) => setSince(e.target.value)}
            aria-label={t("runList.filterFrom")}
          />
        </label>
        <label className="flow-filter">
          <span className="flow-filter-label">{t("runList.filterTo")}</span>
          <input
            type="date"
            value={until}
            min={since || undefined}
            onChange={(e) => setUntil(e.target.value)}
            aria-label={t("runList.filterTo")}
          />
        </label>
        {(since || until) && (
          <Button
            variant="ghost"
            onClick={() => {
              setSince("");
              setUntil("");
            }}
          >
            {t("runList.clearDates")}
          </Button>
        )}
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
          >
            <RotateCcw size={ICON.sm} />
            {retrying ? t("runAction.retrying") : t("runAction.retrySelected")}
          </Button>
          <Button variant="ghost" onClick={() => setSelected(new Set())} disabled={retrying}>
            {t("runList.clearSelection")}
          </Button>
        </div>
      )}

      {error && (
        <ErrorNotice>
          {error}
        </ErrorNotice>
      )}
      {!error && loading && runs.length === 0 && (
        <Loading />
      )}
      {!error && !loading && runs.length === 0 && (
        <Notice>
          {t("runList.empty")}
        </Notice>
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
                      {/* Named by what the row SHOWS — the flow and when it
                          started. It used to be the run id, truncated to 8
                          hex characters, which contradicted this file's own
                          rule two columns over: ids are "plumbing: opaque hex
                          that means nothing to a non-technical user, so they
                          stay off the list". They stayed off the visible list
                          and went to screen readers instead. The start time is
                          what disambiguates, since one flow can have several
                          failed runs here. */}
                      <input
                        type="checkbox"
                        aria-label={t("runList.selectRun", {
                          flow: flowNames[r.graph_id] ?? t("common.unknownParen"),
                          started: formatDateTime(r.enqueued_at),
                        })}
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
                  <td className="run-name-cell">
                    {/* Flow name is the only identifier we show — it's how a
                        user thinks about a run ("the order-alert flow"). The
                        raw ids (flow and run alike) are plumbing: opaque hex
                        that means nothing to a non-technical user, so they
                        stay off the list. A flow whose name we couldn't resolve
                        (deleted, or a failed fetch) shows "(unknown)" rather
                        than leaking its id.

                        It links to the RUN, not the flow: this is a list of
                        runs, so the row's obvious target has to be the thing
                        the row is about — the same way the dashboard's recent
                        runs behave, and the way the flow list leads to flows.
                        Editing is still one click away at the end of the row,
                        but it isn't why anyone opens this page.

                        The link carries the cell's padding instead of the td
                        (see .run-name-cell) so the whole cell is the target,
                        not just the glyph and the text — a row-wide click
                        target isn't available to a <tr>, and this is the part
                        of the row people aim at anyway. */}
                    <Link to={`/runs/${encodeURIComponent(r.id)}`}>
                      <Activity size={ICON.xs} />
                      {flowNames[r.graph_id] ?? t("common.unknownParen")}
                    </Link>
                  </td>
                  <td className="muted" style={{ fontSize: "var(--text-sm)" }}>
                    {formatDateTime(r.enqueued_at)}
                  </td>
                  <td className="muted" style={{ fontSize: "var(--text-sm)" }}>
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
                  <td style={{ textAlign: "right", paddingRight: "var(--space-3)" }}>
                    {showInbox && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => void retryOne(r.id)}
                        disabled={retrying}
                        title={t("runAction.retryTitle")}

                      >
                        <RotateCcw size={ICON.sm} />
                        {retrying ? t("runAction.retrying") : t("runAction.retry")}
                      </Button>
                    )}
                    <Link
                      className="muted"
                      to={`/flows/${encodeURIComponent(r.graph_id)}?run=${encodeURIComponent(r.id)}`}
                      title={t("common.openInEditor")}
                      aria-label={t("common.openInEditor")}
                    >
                      <Pencil size={ICON.sm} />
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
        <Notice>
          {t("runList.noMatches")}
        </Notice>
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
          confirmLabel={t("runAction.retrySelected")}
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


// dayStartISO turns a "YYYY-MM-DD" date (from <input type="date">) into the
// RFC3339 instant for that day's LOCAL midnight — the inclusive ?since= bound.
// Built from numeric parts so it's the user's local day, not UTC (new
// Date("2026-06-27") would parse as UTC midnight and skew the boundary).
function dayStartISO(d: string): string | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(d);
  if (!m) return undefined;
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])).toISOString();
}

// dayEndExclusiveISO turns a "YYYY-MM-DD" end date into local midnight of the
// FOLLOWING day — the exclusive ?until= bound — so the selected end day is
// included in full. Day+1 via the Date constructor handles month/year rollover.
function dayEndExclusiveISO(d: string): string | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(d);
  if (!m) return undefined;
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]) + 1).toISOString();
}

