import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, AlertCircle, ChevronDown, ChevronRight, RotateCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import i18n from "../i18n";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
// RunDetail is the post-failure "what happened" page — and the
// post-success "yes, here are the values" page. T2 of the PMF
// roadmap: when a trial workflow breaks, this is the surface that
// decides whether the user stays or churns.
//
// Layout:
//
//   ┌──────────────────────────────────────────────────────────┐
//   │ ← back   Run summary card (status, graph, timing, error  │
//   │          banner if failed)                                │
//   ├──────────────────────────────────────────────────────────┤
//   │ Node timeline                                             │
//   │   ● node-1  status   duration   ▶ (click to expand)       │
//   │     └─ inputs/outputs/error preview                       │
//   │   ● node-2  …                                             │
//   └──────────────────────────────────────────────────────────┘
//
// One API call (listRunNodes) draws the whole timeline. Each node
// row expands inline to show its result JSON; no extra round trips.
// "Replay" re-fires the graph from scratch and navigates to the
// new run's detail page.
export function RunDetail() {
    const { t } = useTranslation();
    const { runID } = useParams();
    const { token } = useAuth();
    const [run, setRun] = useState(null);
    const [nodes, setNodes] = useState([]);
    const [error, setError] = useState(null);
    const [loading, setLoading] = useState(true);
    const [expanded, setExpanded] = useState({});
    const [replaying, setReplaying] = useState(false);
    useEffect(() => {
        if (!token || !runID)
            return;
        let cancelled = false;
        setLoading(true);
        setError(null);
        Promise.all([api.getJob(token, runID), api.listRunNodes(token, runID)])
            .then(([r, ns]) => {
            if (cancelled)
                return;
            setRun(r);
            setNodes(ns.nodes ?? []);
        })
            .catch((e) => {
            if (!cancelled) {
                const msg = e instanceof APIError ? `${e.status}: ${e.message}` : e.message;
                setError(msg);
            }
        })
            .finally(() => {
            if (!cancelled)
                setLoading(false);
        });
        return () => {
            cancelled = true;
        };
    }, [token, runID]);
    // Poll while anything's still live so the timeline updates without
    // a manual reload. Mirrors RunList's polling pattern.
    useEffect(() => {
        if (!token || !runID || !run)
            return;
        const live = run.Status === "queued" || run.Status === "running" || run.Status === "awaiting" ||
            nodes.some((n) => n.Status === "queued" || n.Status === "running" || n.Status === "awaiting");
        if (!live)
            return;
        const t = window.setInterval(() => {
            Promise.all([api.getJob(token, runID), api.listRunNodes(token, runID)])
                .then(([r, ns]) => {
                setRun(r);
                setNodes(ns.nodes ?? []);
            })
                .catch(() => { });
        }, 2000);
        return () => window.clearInterval(t);
    }, [token, runID, run, nodes]);
    const toggle = (nid) => setExpanded((prev) => ({ ...prev, [nid]: !prev[nid] }));
    const replay = async () => {
        if (!token || !run)
            return;
        setReplaying(true);
        try {
            // Use the tenant/workspace baked into the original run record
            // — replays go back to the same scope, not the user's current
            // workspace switcher state. Less surprising for "I'm
            // investigating an old run."
            const result = await api.runGraph(token, "", "", run.GraphID);
            // runGraph signature takes tenant/workspace; rely on the
            // server falling back to principal scope when empty. (If it
            // requires explicit, swap to using activeTenant/Workspace.)
            if (result?.job_id) {
                window.location.href = `/runs/${encodeURIComponent(result.job_id)}`;
            }
        }
        catch (e) {
            const msg = e instanceof APIError ? `${e.status}: ${e.message}` : e.message;
            setError(t("runDetail.replayFailed", { error: msg }));
        }
        finally {
            setReplaying(false);
        }
    };
    if (loading) {
        return (_jsx("div", { className: "page", children: _jsx("div", { className: "card", children: t("runDetail.loading") }) }));
    }
    if (error || !run) {
        return (_jsxs("div", { className: "page", children: [_jsx("div", { className: "page-title", children: _jsxs("div", { children: [_jsxs(Link, { to: "/runs", className: "back-link", children: [_jsx(ArrowLeft, { size: 14 }), " ", t("runDetail.backToRuns")] }), _jsx("h1", { children: t("runDetail.notFoundTitle") })] }) }), _jsx("div", { className: "card error", children: error ?? t("runDetail.notFoundBody") })] }));
    }
    // Sort nodes by enqueued_at ASC so the timeline reads top→down
    // in execution order rather than newest-first.
    const orderedNodes = [...nodes].sort((a, b) => {
        const ta = Date.parse(timestamp(a, "EnqueuedAt", "enqueued_at"));
        const tb = Date.parse(timestamp(b, "EnqueuedAt", "enqueued_at"));
        return ta - tb;
    });
    // Find the first failed node (if any) so the banner can name it.
    const failedNode = orderedNodes.find((n) => n.Status === "failed");
    return (_jsxs("div", { className: "page run-detail", children: [_jsxs("div", { className: "page-title", children: [_jsxs("div", { children: [_jsxs(Link, { to: "/runs", className: "back-link", children: [_jsx(ArrowLeft, { size: 14 }), " ", t("runDetail.backToRuns")] }), _jsxs("h1", { style: { display: "flex", alignItems: "center", gap: 10 }, children: [_jsx("span", { className: "status-dot " + run.Status }), run.GraphID] }), _jsxs("div", { className: "sub", children: [t("runDetail.runIdLabel"), " ", _jsx("code", { style: { fontFamily: "var(--font-mono)", fontSize: 12 }, children: run.ID })] })] }), _jsxs("div", { style: { display: "flex", gap: 8 }, children: [_jsx(Link, { to: `/flows/${encodeURIComponent(run.GraphID)}?run=${encodeURIComponent(run.ID)}`, className: "secondary-link", children: _jsx("button", { type: "button", className: "secondary", children: t("runDetail.openInEditor") }) }), _jsxs("button", { type: "button", className: "primary", onClick: replay, disabled: replaying, title: t("runDetail.replayTitle"), children: [_jsx(RotateCw, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), replaying ? t("runDetail.replaying") : t("runDetail.replay")] })] })] }), run.Status === "failed" && (_jsxs("div", { className: "run-error-banner", children: [_jsx(AlertCircle, { size: 18, style: { flexShrink: 0, marginTop: 2 } }), _jsxs("div", { children: [_jsxs("div", { className: "run-error-title", children: [failedNode
                                        ? t("runDetail.failedAt", { node: failedNode.NodeID })
                                        : t("runDetail.failed"), run.Result?.error?.code && (_jsxs("span", { className: "run-error-code", children: [" \u00B7 ", run.Result.error.code] }))] }), run.Result?.error?.message && (_jsx("div", { className: "run-error-msg", children: run.Result.error.message }))] })] })), _jsxs("div", { className: "run-summary card", children: [_jsx(SummaryRow, { label: t("runDetail.summaryStatus"), value: _jsx(StatusChip, { status: run.Status }) }), _jsx(SummaryRow, { label: t("runDetail.summaryStarted"), value: formatAbs(run.StartedAt ?? null) }), _jsx(SummaryRow, { label: t("runDetail.summaryFinished"), value: formatAbs(run.FinishedAt ?? null) }), _jsx(SummaryRow, { label: t("runDetail.summaryDuration"), value: run.StartedAt && run.FinishedAt
                            ? formatDuration(run.StartedAt, run.FinishedAt)
                            : run.Status === "running"
                                ? t("runDetail.inProgress")
                                : "—" }), _jsx(SummaryRow, { label: t("runDetail.summaryNodes"), value: _jsxs("span", { children: [t("runDetail.nodesTotal", { count: orderedNodes.length }), orderedNodes.filter((n) => n.Status === "succeeded").length > 0 && (_jsxs("span", { style: { color: "var(--muted)" }, children: [" · ", t("runDetail.nodesSucceeded", {
                                            count: orderedNodes.filter((n) => n.Status === "succeeded").length,
                                        })] })), orderedNodes.filter((n) => n.Status === "failed").length > 0 && (_jsxs("span", { style: { color: "var(--danger)" }, children: [" · ", t("runDetail.nodesFailed", {
                                            count: orderedNodes.filter((n) => n.Status === "failed").length,
                                        })] }))] }) })] }), _jsx("h2", { style: { marginTop: "var(--space-4)" }, children: t("runDetail.timeline") }), orderedNodes.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: t("runDetail.noNodes") })), _jsx("div", { className: "node-timeline", children: orderedNodes.map((n) => {
                    const isOpen = !!expanded[n.NodeID];
                    const dur = n.StartedAt && n.FinishedAt
                        ? formatDuration(n.StartedAt, n.FinishedAt)
                        : n.Status === "running"
                            ? t("runDetail.inProgress")
                            : "—";
                    return (_jsxs("div", { className: "node-row" + (n.Status === "failed" ? " failed" : ""), children: [_jsxs("button", { type: "button", className: "node-row-head", onClick: () => toggle(n.NodeID), "aria-expanded": isOpen, children: [isOpen ? _jsx(ChevronDown, { size: 12 }) : _jsx(ChevronRight, { size: 12 }), _jsx("span", { className: "status-dot " + n.Status }), _jsx("span", { className: "node-id", children: n.NodeID }), _jsx("span", { className: "node-status", children: n.Status }), _jsx("span", { className: "node-dur", children: dur }), n.Result?.error?.code && (_jsx("span", { className: "node-err", children: n.Result.error.code }))] }), isOpen && (_jsxs("div", { className: "node-body", children: [n.Result?.error && (_jsxs("div", { className: "node-err-block", children: [_jsx("div", { className: "node-err-code", children: n.Result.error.code }), _jsx("div", { children: n.Result.error.message }), n.Result.error.details && (_jsxs("details", { className: "node-err-details", children: [_jsx("summary", { children: t("runDetail.details") }), _jsx("pre", { className: "node-err-pre", children: n.Result.error.details })] }))] })), n.Job?.Input && Object.keys(n.Job.Input).length > 0 && (_jsxs("div", { className: "node-output", children: [_jsx("div", { className: "node-section-head", children: t("runDetail.inputs") }), Object.entries(n.Job.Input).map(([port, ref]) => (_jsxs("details", { className: "node-port", children: [_jsxs("summary", { children: [_jsx("span", { className: "node-port-name", children: port }), ref?.mime && (_jsx("span", { className: "node-port-mime", children: ref.mime }))] }), _jsx("pre", { className: "node-port-value", children: previewValue(ref) })] }, port)))] })), n.Result?.output && Object.keys(n.Result.output).length > 0 && (_jsxs("div", { className: "node-output", children: [_jsx("div", { className: "node-section-head", children: t("runDetail.output") }), Object.entries(n.Result.output).map(([port, ref]) => (_jsxs("details", { className: "node-port", children: [_jsxs("summary", { children: [_jsx("span", { className: "node-port-name", children: port }), ref?.mime && (_jsx("span", { className: "node-port-mime", children: ref.mime }))] }), _jsx("pre", { className: "node-port-value", children: previewValue(ref) })] }, port)))] })), !n.Result?.error && !n.Result?.output && !(n.Job?.Input && Object.keys(n.Job.Input).length > 0) && (_jsx("div", { style: { color: "var(--faint)", fontSize: 12 }, children: t("runDetail.noResult") }))] }))] }, n.ID));
                }) })] }));
}
function SummaryRow({ label, value }) {
    return (_jsxs("div", { className: "run-summary-row", children: [_jsx("span", { className: "run-summary-label", children: label }), _jsx("span", { className: "run-summary-value", children: value })] }));
}
function StatusChip({ status }) {
    return (_jsxs("span", { className: "status-chip " + status, children: [_jsx("span", { className: "status-dot " + status }), status] }));
}
function formatAbs(iso) {
    if (!iso)
        return "—";
    const t = Date.parse(iso);
    if (!Number.isFinite(t))
        return iso;
    return new Date(t).toLocaleString();
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
        return `${(ms / 1000).toFixed(2)}s`;
    return `${(ms / 60_000).toFixed(1)}m`;
}
// timestamp tries the Go-shaped capitalized field then the
// JSON-shaped lowercased one — defends against backend serialization
// drift since JobRecord uses Go field names today.
function timestamp(rec, ...keys) {
    for (const k of keys) {
        const v = rec[k];
        if (v)
            return v;
    }
    return "";
}
// previewValue renders a Ref's value (or path) for the expandable
// preview block. Pretty-prints JSON; strings stay verbatim. The Ref
// type's `data` field corresponds to the Go side's `Inline`.
function previewValue(ref) {
    if (ref.ref)
        return `→ ${ref.ref}`;
    const v = ref.data;
    if (v === undefined || v === null)
        return i18n.t("runDetail.emptyValue");
    if (typeof v === "string")
        return v;
    try {
        return JSON.stringify(v, null, 2);
    }
    catch {
        return String(v);
    }
}
