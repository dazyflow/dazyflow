import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Activity, ExternalLink } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
// RunList shows every run across every graph in the principal's
// workspace. Filterable by status, paginated with a Load-more button,
// and live-polls the first page when something is mid-flight. Each row
// links to the editor for that graph with the run pre-selected so the
// canvas pre-fills with that run's node statuses + output preview.
const STATUS_CHIPS = [
    { label: "All", value: "" },
    { label: "Running", value: "running" },
    { label: "Awaiting", value: "awaiting" },
    { label: "Failed", value: "failed" },
    { label: "Succeeded", value: "succeeded" },
];
const PAGE_SIZE = 50;
export function RunList() {
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
    return (_jsxs("div", { children: [_jsx("div", { className: "page-title", children: _jsxs("div", { children: [_jsx("h1", { children: "Runs" }), _jsxs("div", { className: "sub", children: ["All runs in ", activeTenant || me?.tenant, "/", activeWorkspace || me?.workspace || "(any)"] })] }) }), _jsx("div", { className: "run-history-filters", style: { marginBottom: "var(--space-4)" }, children: STATUS_CHIPS.map((c) => (_jsx("button", { type: "button", className: "run-filter-chip" + (filter === c.value ? " active" : ""), onClick: () => setFilter(c.value), children: c.label }, c.label))) }), error && (_jsx("div", { className: "card", style: { color: "var(--danger)" }, children: error })), !error && loading && runs.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: "Loading\u2026" })), !error && !loading && runs.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: "No runs match this filter." })), runs.length > 0 && (_jsx("div", { className: "card", style: { padding: 0, overflow: "hidden" }, children: _jsxs("table", { className: "run-table", children: [_jsx("thead", { children: _jsxs("tr", { children: [_jsx("th", { style: { width: 28 } }), _jsx("th", { children: "Run" }), _jsx("th", { children: "Flow" }), _jsx("th", { children: "Started" }), _jsx("th", { children: "Duration" }), _jsx("th", {})] }) }), _jsx("tbody", { children: runs.map((r) => (_jsxs("tr", { children: [_jsx("td", { children: _jsx("span", { className: "status-dot " + r.status }) }), _jsx("td", { style: { fontFamily: "var(--font-mono)", fontSize: 12 }, children: r.id.slice(0, 12) }), _jsx("td", { children: _jsxs(Link, { to: `/flows/${encodeURIComponent(r.graph_id)}?run=${encodeURIComponent(r.id)}`, style: {
                                                display: "inline-flex",
                                                alignItems: "center",
                                                gap: 4,
                                            }, children: [_jsx(Activity, { size: 12 }), r.graph_id] }) }), _jsx("td", { style: { color: "var(--muted)", fontSize: 12 }, children: formatTime(r.enqueued_at) }), _jsxs("td", { style: { color: "var(--muted)", fontSize: 12 }, children: [r.started_at && r.finished_at
                                                ? formatDuration(r.started_at, r.finished_at)
                                                : r.status === "running"
                                                    ? "in progress"
                                                    : "—", r.error_code && (_jsxs("span", { style: { color: "var(--danger)", marginLeft: 6 }, children: ["\u00B7 ", r.error_code] }))] }), _jsx("td", { style: { textAlign: "right", paddingRight: 12 }, children: _jsx(Link, { to: `/flows/${encodeURIComponent(r.graph_id)}?run=${encodeURIComponent(r.id)}`, style: { color: "var(--muted)" }, children: _jsx(ExternalLink, { size: 14 }) }) })] }, r.id))) })] }) })), hasMore && (_jsx("div", { style: { textAlign: "center", marginTop: "var(--space-4)" }, children: _jsx("button", { onClick: loadMore, disabled: loading, children: loading ? "Loading…" : "Load more" }) }))] }));
}
function formatTime(iso) {
    const t = Date.parse(iso);
    if (!Number.isFinite(t))
        return iso;
    const diffSec = Math.max(0, Math.round((Date.now() - t) / 1000));
    if (diffSec < 60)
        return `${diffSec}s ago`;
    if (diffSec < 3600)
        return `${Math.round(diffSec / 60)}m ago`;
    if (diffSec < 86400)
        return `${Math.round(diffSec / 3600)}h ago`;
    return `${Math.round(diffSec / 86400)}d ago`;
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
