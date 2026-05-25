import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
export function OutputPreview({ runID, nodeID, refreshKey }) {
    const { token } = useAuth();
    const [rec, setRec] = useState(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState(null);
    useEffect(() => {
        if (!token)
            return;
        let cancelled = false;
        setLoading(true);
        setError(null);
        api
            .getNodeRecord(token, runID, nodeID)
            .then((r) => {
            if (!cancelled)
                setRec(r);
        })
            .catch((e) => {
            if (!cancelled) {
                // 404 just means "this node hasn't run yet in this run" —
                // show a calm empty state, not a red error.
                if (e instanceof APIError && e.status === 404) {
                    setRec(null);
                    setError(null);
                }
                else {
                    setError(e.message);
                }
            }
        })
            .finally(() => {
            if (!cancelled)
                setLoading(false);
        });
        return () => {
            cancelled = true;
        };
    }, [token, runID, nodeID, refreshKey]);
    if (loading) {
        return _jsx("div", { style: { color: "var(--muted)", fontSize: 12 }, children: "Loading\u2026" });
    }
    if (error) {
        return _jsx("div", { style: { color: "var(--danger)", fontSize: 12 }, children: error });
    }
    if (!rec) {
        return (_jsx("div", { style: { color: "var(--faint)", fontSize: 12, fontStyle: "italic" }, children: "No run output for this node yet. Click Run to execute." }));
    }
    return (_jsxs("div", { className: "output-preview", children: [_jsxs("div", { className: "output-row", children: [_jsx("span", { className: "status-dot " + rec.Status }), _jsx("span", { style: { fontSize: 13, fontWeight: 500 }, children: rec.Status }), rec.Attempt && rec.Attempt > 1 && (_jsxs("span", { style: { fontSize: 11, color: "var(--faint)" }, children: ["attempt ", rec.Attempt] })), rec.FinishedAt && (_jsx("span", { style: { fontSize: 11, color: "var(--faint)", marginLeft: "auto" }, children: formatRelative(rec.FinishedAt) }))] }), rec.Result?.error && _jsx(ErrorBlock, { error: rec.Result.error }), rec.Result?.output && Object.keys(rec.Result.output).length > 0 && (_jsx(PortList, { output: rec.Result.output })), rec.Result?.output && Object.keys(rec.Result.output).length === 0 && !rec.Result.error && (_jsx("div", { style: { fontSize: 12, color: "var(--faint)", fontStyle: "italic" }, children: "(no output ports emitted)" }))] }));
}
function PortList({ output }) {
    const entries = Object.entries(output);
    return (_jsx("div", { className: "port-list", children: entries.map(([port, ref]) => (_jsx(PortCard, { port: port, ref0: ref }, port))) }));
}
function PortCard({ port, ref0 }) {
    const [expanded, setExpanded] = useState(false);
    const value = ref0.data;
    const isPrimitive = value === null ||
        typeof value === "string" ||
        typeof value === "number" ||
        typeof value === "boolean";
    // For very large payloads, collapse by default and offer expand.
    const json = formatValue(value);
    const truncatedThreshold = 800;
    const tooBig = json.length > truncatedThreshold;
    const display = expanded || !tooBig ? json : json.slice(0, truncatedThreshold) + "\n…";
    return (_jsxs("div", { className: "port-card", children: [_jsxs("div", { className: "port-head", children: [_jsx("span", { className: "port-name", children: port }), ref0.mime && _jsx("span", { className: "port-mime", children: ref0.mime })] }), isPrimitive && typeof value === "string" ? (_jsx("pre", { className: "port-value", children: value })) : (_jsx("pre", { className: "port-value", children: display })), tooBig && (_jsx("button", { type: "button", className: "ghost", style: { fontSize: 11, padding: "2px 8px", marginTop: 4 }, onClick: () => setExpanded((v) => !v), children: expanded ? "Collapse" : `Show all (${json.length.toLocaleString()} chars)` }))] }));
}
function ErrorBlock({ error }) {
    return (_jsxs("div", { className: "port-card port-error", children: [_jsxs("div", { className: "port-head", children: [_jsx("span", { className: "port-name", children: "error" }), _jsx("span", { className: "port-mime", children: error.code })] }), _jsx("pre", { className: "port-value", children: error.message })] }));
}
function formatValue(v) {
    if (v === undefined)
        return "(empty)";
    try {
        return JSON.stringify(v, null, 2);
    }
    catch {
        return String(v);
    }
}
// formatRelative produces a short, human-friendly age string like "2s
// ago" or "5m ago". Falls back to the raw timestamp on parse failure.
function formatRelative(iso) {
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
