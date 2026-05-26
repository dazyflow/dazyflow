import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useRef, useState } from "react";
import { ChevronDown, History } from "lucide-react";
import { useTranslation } from "react-i18next";
import i18n from "../i18n";
import { useAuth } from "../auth";
import { api } from "../api";
// Status filter chips — single-select, "All" clears. Labels are i18n
// keys resolved at render time so the chips track the active locale.
const STATUS_CHIPS = [
    { labelKey: "runList.filterAll", value: "" },
    { labelKey: "runList.filterRunning", value: "running" },
    { labelKey: "runList.filterFailed", value: "failed" },
    { labelKey: "runList.filterSucceeded", value: "succeeded" },
];
const PAGE_SIZE = 20;
export function RunHistory({ tenant, workspace, graphID, currentRunID, onSelect, }) {
    const { t } = useTranslation();
    const { token } = useAuth();
    const [open, setOpen] = useState(false);
    const [runs, setRuns] = useState([]);
    const [loading, setLoading] = useState(false);
    const [filter, setFilter] = useState("");
    const [hasMore, setHasMore] = useState(false);
    const popRef = useRef(null);
    // Click-outside closes the popover. Listening on mousedown (not
    // click) is important so a fresh trigger-click can re-open it without
    // racing the close handler.
    useEffect(() => {
        if (!open)
            return;
        const onDown = (e) => {
            if (popRef.current && !popRef.current.contains(e.target)) {
                setOpen(false);
            }
        };
        document.addEventListener("mousedown", onDown);
        return () => document.removeEventListener("mousedown", onDown);
    }, [open]);
    // Reload from page 0 whenever the filter changes or the popover opens.
    useEffect(() => {
        if (!open || !token)
            return;
        let cancelled = false;
        setLoading(true);
        api
            .listRuns(token, tenant, workspace, graphID, {
            limit: PAGE_SIZE,
            status: filter || undefined,
        })
            .then((r) => {
            if (cancelled)
                return;
            const page = r.runs ?? [];
            setRuns(page);
            setHasMore(page.length === PAGE_SIZE);
        })
            .catch(() => {
            /* leave list empty — error already surfaced higher up */
        })
            .finally(() => {
            if (!cancelled)
                setLoading(false);
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
        if (!open || !token)
            return;
        const anyLive = runs.some((r) => r.status === "queued" ||
            r.status === "running" ||
            r.status === "awaiting");
        if (!anyLive)
            return;
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
        if (!token || loading)
            return;
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
        }
        finally {
            setLoading(false);
        }
    };
    const currentRun = runs.find((r) => r.id === currentRunID);
    const currentStatus = currentRun?.status;
    return (_jsxs("div", { ref: popRef, style: { position: "relative" }, children: [_jsxs("button", { type: "button", className: "ghost", onClick: () => setOpen((v) => !v), style: { display: "inline-flex", alignItems: "center", gap: 6 }, children: [_jsx(History, { size: 14 }), currentRunID ? (_jsxs(_Fragment, { children: [currentStatus && (_jsx("span", { className: "status-dot " + currentStatus })), _jsx("span", { style: { fontFamily: "var(--font-mono)", fontSize: 12 }, children: currentRunID.slice(0, 8) })] })) : (_jsx("span", { style: { fontSize: 12, color: "var(--muted)" }, children: t("runHistory.noRun") })), _jsx(ChevronDown, { size: 12 })] }), open && (_jsxs("div", { className: "run-history-pop", children: [_jsx("div", { className: "run-history-head", children: t("runHistory.head") }), _jsx("div", { className: "run-history-filters", children: STATUS_CHIPS.map((c) => (_jsx("button", { type: "button", className: "run-filter-chip" + (filter === c.value ? " active" : ""), onClick: () => setFilter(c.value), children: t(c.labelKey) }, c.labelKey))) }), loading && runs.length === 0 && (_jsx("div", { className: "run-history-empty", children: t("common.loading") })), !loading && runs.length === 0 && (_jsx("div", { className: "run-history-empty", children: t("runHistory.empty") })), runs.map((r) => (_jsxs("button", { type: "button", className: "run-history-row" + (r.id === currentRunID ? " active" : ""), onClick: () => {
                            onSelect(r.id);
                            setOpen(false);
                        }, children: [_jsx("span", { className: "status-dot " + r.status }), _jsxs("div", { style: {
                                    display: "flex",
                                    flexDirection: "column",
                                    minWidth: 0,
                                    flex: 1,
                                }, children: [_jsx("span", { style: { fontFamily: "var(--font-mono)", fontSize: 12 }, children: r.id.slice(0, 12) }), _jsxs("span", { style: { fontSize: 11, color: "var(--faint)" }, title: r.enqueued_at, children: [formatTime(r.enqueued_at), r.finished_at && r.started_at && (_jsxs(_Fragment, { children: [" \u00B7 ", formatDuration(r.started_at, r.finished_at)] })), r.error_code && _jsxs(_Fragment, { children: [" \u00B7 ", r.error_code] })] })] })] }, r.id))), hasMore && (_jsx("button", { type: "button", className: "ghost run-load-more", disabled: loading, onClick: loadMore, children: loading ? t("common.loading") : t("runHistory.loadMore") }))] }))] }));
}
function formatTime(iso) {
    const ts = Date.parse(iso);
    if (!Number.isFinite(ts))
        return iso;
    const diffSec = Math.max(0, Math.round((Date.now() - ts) / 1000));
    if (diffSec < 60)
        return i18n.t("runList.secondsAgo", { count: diffSec });
    if (diffSec < 3600)
        return i18n.t("runList.minutesAgo", { count: Math.round(diffSec / 60) });
    if (diffSec < 86400)
        return i18n.t("runList.hoursAgo", { count: Math.round(diffSec / 3600) });
    return i18n.t("runList.daysAgo", { count: Math.round(diffSec / 86400) });
}
function formatDuration(startedISO, finishedISO) {
    const start = Date.parse(startedISO);
    const end = Date.parse(finishedISO);
    if (!Number.isFinite(start) || !Number.isFinite(end))
        return "";
    const ms = Math.max(0, end - start);
    if (ms < 1000)
        return `${ms}ms`;
    if (ms < 60_000)
        return `${(ms / 1000).toFixed(1)}s`;
    return `${Math.round(ms / 60_000)}m`;
}
