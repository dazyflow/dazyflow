import { useEffect, useRef, useState } from "react";
import { ChevronDown, History } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import type { RunSummary, JobStatus } from "../types";
import { absoluteTime, formatDateTime } from "../lib/datetime";

// RunHistory shows the current run (status dot + short ID) and opens a
// dropdown listing recent runs on click. Picking one calls onSelect,
// which in FlowEditor re-subscribes the SSE stream so node statuses
// + output previews reflect the chosen run.
type Props = {
  tenant: string;
  workspace: string;
  graphID: string;
  currentRunID: string | null;
  onSelect: (runID: string) => void;
};

// Status filter chips — single-select, "All" clears. Labels are i18n
// keys resolved at render time so the chips track the active locale.
const STATUS_CHIPS: { labelKey: string; value: JobStatus | "" }[] = [
  { labelKey: "runList.filterAll", value: "" },
  { labelKey: "runList.filterRunning", value: "running" },
  { labelKey: "runList.filterFailed", value: "failed" },
  { labelKey: "runList.filterSucceeded", value: "succeeded" },
];

const PAGE_SIZE = 20;

export function RunHistory({
  tenant,
  workspace,
  graphID,
  currentRunID,
  onSelect,
}: Props) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [open, setOpen] = useState(false);
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState<JobStatus | "">("");
  const [hasMore, setHasMore] = useState(false);
  const popRef = useRef<HTMLDivElement | null>(null);

  // Click-outside closes the popover. Listening on mousedown (not
  // click) is important so a fresh trigger-click can re-open it without
  // racing the close handler.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (popRef.current && !popRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  // Reload from page 0 whenever the filter changes or the popover opens.
  useEffect(() => {
    if (!open || !token) return;
    let cancelled = false;
    setLoading(true);
    api
      .listRuns(token, tenant, workspace, graphID, {
        limit: PAGE_SIZE,
        status: filter || undefined,
      })
      .then((r) => {
        if (cancelled) return;
        const page = r.runs ?? [];
        setRuns(page);
        setHasMore(page.length === PAGE_SIZE);
      })
      .catch(() => {
        /* leave list empty — error already surfaced higher up */
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, filter, token, tenant, workspace, graphID]);

  // Live polling: while the dropdown is open and at least one row is
  // non-terminal (queued/running/awaiting), refresh the FIRST page
  // every 3 seconds so the visible dots track reality. Stops as soon
  // as the dropdown closes or all visible rows reach terminal state.
  useEffect(() => {
    if (!open || !token) return;
    const anyLive = runs.some(
      (r) =>
        r.status === "queued" ||
        r.status === "running" ||
        r.status === "awaiting",
    );
    if (!anyLive) return;
    const t = window.setInterval(() => {
      api
        .listRuns(token, tenant, workspace, graphID, {
          limit: Math.max(PAGE_SIZE, runs.length),
          status: filter || undefined,
        })
        .then((r) => {
          const page = r.runs ?? [];
          setRuns(page);
          setHasMore(page.length >= PAGE_SIZE);
        })
        .catch(() => {
          /* ignore — next tick retries */
        });
    }, 3000);
    return () => window.clearInterval(t);
  }, [open, token, tenant, workspace, graphID, filter, runs]);

  const loadMore = async () => {
    if (!token || loading) return;
    setLoading(true);
    try {
      const r = await api.listRuns(token, tenant, workspace, graphID, {
        limit: PAGE_SIZE,
        offset: runs.length,
        status: filter || undefined,
      });
      const next = r.runs ?? [];
      setRuns((prev) => [...prev, ...next]);
      setHasMore(next.length === PAGE_SIZE);
    } finally {
      setLoading(false);
    }
  };

  const currentRun = runs.find((r) => r.id === currentRunID);
  const currentStatus: JobStatus | undefined = currentRun?.status;

  return (
    <div ref={popRef} style={{ position: "relative" }}>
      <button
        type="button"
        className="ghost"
        onClick={() => setOpen((v) => !v)}
        style={{ display: "inline-flex", alignItems: "center", gap: 6 }}
      >
        <History size={14} />
        {currentRunID ? (
          <>
            {currentStatus && (
              <span className={"status-dot " + currentStatus} />
            )}
            <span style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)" }}>
              {currentRunID.slice(0, 8)}
            </span>
          </>
        ) : (
          <span style={{ fontSize: "var(--text-sm)", color: "var(--muted)" }}>{t("runHistory.noRun")}</span>
        )}
        <ChevronDown size={12} />
      </button>
      {open && (
        <div className="run-history-pop">
          <div className="run-history-head">{t("runHistory.head")}</div>
          <div className="run-history-filters">
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
          {loading && runs.length === 0 && (
            <div className="run-history-empty">{t("common.loading")}</div>
          )}
          {!loading && runs.length === 0 && (
            <div className="run-history-empty">{t("runHistory.empty")}</div>
          )}
          {runs.map((r) => (
            <button
              key={r.id}
              type="button"
              className={
                "run-history-row" + (r.id === currentRunID ? " active" : "")
              }
              onClick={() => {
                onSelect(r.id);
                setOpen(false);
              }}
            >
              <span className={"status-dot " + r.status} />
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  minWidth: 0,
                  flex: 1,
                }}
              >
                <span style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)" }}>
                  {r.id.slice(0, 12)}
                </span>
                <span
                  style={{ fontSize: "var(--text-xs)", color: "var(--faint)" }}
                  title={absoluteTime(r.enqueued_at)}
                >
                  {formatTime(r.enqueued_at)}
                  {r.finished_at && r.started_at && (
                    <> · {formatDuration(r.started_at, r.finished_at)}</>
                  )}
                  {r.error_code && <> · {r.error_code}</>}
                </span>
              </div>
            </button>
          ))}
          {hasMore && (
            <button
              type="button"
              className="ghost run-load-more"
              disabled={loading}
              onClick={loadMore}
            >
              {loading ? t("common.loading") : t("runHistory.loadMore")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

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
