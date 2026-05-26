import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Activity, ExternalLink } from "lucide-react";
import { useTranslation } from "react-i18next";
import i18n from "../i18n";
import { useAuth } from "../auth";
import { api } from "../api";
const PAGE_SIZE = 50;
export function RunList() {
    const { t } = useTranslation();
    // Status filter chips. Label keys (not literals) are resolved against
    // i18n at render time so the chips switch with the active locale.
    const STATUS_CHIPS = [
        { labelKey: "runList.filterAll", value: "" },
        { labelKey: "runList.filterRunning", value: "running" },
        { labelKey: "runList.filterAwaiting", value: "awaiting" },
        { labelKey: "runList.filterFailed", value: "failed" },
        { labelKey: "runList.filterSucceeded", value: "succeeded" },
    ];
    const { token, me, activeTenant, activeWorkspace } = useAuth();
    const [runs, setRuns] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [filter, setFilter] = useState("");
    const [hasMore, setHasMore] = useState(false);
    useEffect(() => {
        if (!token)
            return;
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
            if (cancelled)
                return;
            const page = r.runs ?? [];
            setRuns(page);
            setHasMore(page.length === PAGE_SIZE);
        })
            .catch((e) => {
            if (!cancelled)
                setError(e.message);
        })
            .finally(() => {
            if (!cancelled)
                setLoading(false);
        });
        return () => {
            cancelled = true;
        };
    }, [token, filter, activeWorkspace, activeTenant]);
    // Live polling whenever anything is in-flight. Same heuristic as the
    // RunHistory dropdown — refresh only the first PAGE_SIZE rows so a
    // long scrollback isn't repeatedly fetched.
    useEffect(() => {
        if (!token)
            return;
        const anyLive = runs.some((r) => r.status === "queued" ||
            r.status === "running" ||
            r.status === "awaiting");
        if (!anyLive)
            return;
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
                .catch(() => { });
        }, 3000);
        return () => window.clearInterval(t);
    }, [token, runs, filter, activeWorkspace, activeTenant]);
    const loadMore = async () => {
        if (!token || loading)
            return;
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
        }
        finally {
            setLoading(false);
        }
    };
    return (_jsxs("div", { children: [_jsx("div", { className: "page-title", children: _jsxs("div", { children: [_jsx("h1", { children: t("runList.title") }), _jsx("div", { className: "sub", children: t("runList.subtitle", {
                                tenant: activeTenant || me?.tenant,
                                workspace: activeWorkspace || me?.workspace || t("runList.anyWorkspace"),
                            }) })] }) }), _jsx("div", { className: "run-history-filters", style: { marginBottom: "var(--space-4)" }, children: STATUS_CHIPS.map((c) => (_jsx("button", { type: "button", className: "run-filter-chip" + (filter === c.value ? " active" : ""), onClick: () => setFilter(c.value), children: t(c.labelKey) }, c.labelKey))) }), error && (_jsx("div", { className: "card", style: { color: "var(--danger)" }, children: error })), !error && loading && runs.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: t("common.loading") })), !error && !loading && runs.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: t("runList.empty") })), runs.length > 0 && (_jsx("div", { className: "card", style: { padding: 0, overflow: "hidden" }, children: _jsxs("table", { className: "run-table", children: [_jsx("thead", { children: _jsxs("tr", { children: [_jsx("th", { style: { width: 28 } }), _jsx("th", { children: t("runList.colRun") }), _jsx("th", { children: t("runList.colFlow") }), _jsx("th", { children: t("runList.colStarted") }), _jsx("th", { children: t("runList.colDuration") }), _jsx("th", {})] }) }), _jsx("tbody", { children: runs.map((r) => (_jsxs("tr", { children: [_jsx("td", { children: _jsx("span", { className: "status-dot " + r.status }) }), _jsx("td", { style: { fontFamily: "var(--font-mono)", fontSize: 12 }, children: _jsx(Link, { to: `/runs/${encodeURIComponent(r.id)}`, style: { textDecoration: "none" }, children: r.id.slice(0, 12) }) }), _jsx("td", { children: _jsxs(Link, { to: `/flows/${encodeURIComponent(r.graph_id)}?run=${encodeURIComponent(r.id)}`, style: {
                                                display: "inline-flex",
                                                alignItems: "center",
                                                gap: 4,
                                            }, children: [_jsx(Activity, { size: 12 }), r.graph_id] }) }), _jsx("td", { style: { color: "var(--muted)", fontSize: 12 }, children: formatTime(r.enqueued_at) }), _jsxs("td", { style: { color: "var(--muted)", fontSize: 12 }, children: [r.started_at && r.finished_at
                                                ? formatDuration(r.started_at, r.finished_at)
                                                : r.status === "running"
                                                    ? t("runList.inProgress")
                                                    : "—", r.error_code && (_jsxs("span", { style: { color: "var(--danger)", marginLeft: 6 }, children: ["\u00B7 ", r.error_code] }))] }), _jsx("td", { style: { textAlign: "right", paddingRight: 12 }, children: _jsx(Link, { to: `/runs/${encodeURIComponent(r.id)}`, style: { color: "var(--muted)" }, title: t("runList.openDetails"), children: _jsx(ExternalLink, { size: 14 }) }) })] }, r.id))) })] }) })), hasMore && (_jsx("div", { style: { textAlign: "center", marginTop: "var(--space-4)" }, children: _jsx("button", { onClick: loadMore, disabled: loading, children: loading ? t("common.loading") : t("runList.loadMore") }) }))] }));
}
// formatTime renders a relative time string ("3m ago", "2h ago", …).
// Pulls the active locale via the singleton i18n instance — avoids
// threading `t` through table-row helpers.
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
