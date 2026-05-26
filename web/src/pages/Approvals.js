import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { CheckCircle2, XCircle, Workflow, Inbox } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import i18n from "../i18n";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
// Approvals is the inbox for await_approval nodes parked across the
// workspace. Polls every 5s so a freshly-pending node shows up without
// manual refresh. Approve / Reject buttons call POST /approvals/...
// which Service.Approve services (same code path as the HMAC endpoint).
export function Approvals() {
    const { t } = useTranslation();
    const { token, me, activeTenant, activeWorkspace } = useAuth();
    const [items, setItems] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    // Track in-flight approve/reject per row so users can't double-click.
    // The key is `${runID}/${nodeID}`. Once a decision posts the row
    // disappears on the next refresh.
    const [acting, setActing] = useState({});
    const refresh = useCallback(async () => {
        if (!token)
            return;
        try {
            // Narrow to the workspace currently selected in the switcher so
            // an admin's inbox tracks the rest of the UI. Empty string =
            // tenant-wide view (returns everything the principal can see).
            const r = await api.listPendingApprovals(token, {
                workspace: activeWorkspace || undefined,
                tenant: activeTenant || undefined,
            });
            setItems(r.approvals ?? []);
            setError(null);
        }
        catch (e) {
            setError(e.message);
        }
        finally {
            setLoading(false);
        }
    }, [token, activeTenant, activeWorkspace]);
    useEffect(() => {
        void refresh();
    }, [refresh]);
    // Live polling — 5 seconds is light enough to feel responsive but
    // doesn't hammer the daemon. Stops only when the page unmounts.
    useEffect(() => {
        const t = window.setInterval(() => {
            void refresh();
        }, 5000);
        return () => window.clearInterval(t);
    }, [refresh]);
    const decide = async (item, decision) => {
        if (!token)
            return;
        const key = `${item.run_id}/${item.node_id}`;
        setActing((s) => ({ ...s, [key]: decision }));
        try {
            // Optional comment prompt only on reject — the common-case
            // approval is one-click. Reject benefits from a "why".
            let comment;
            if (decision === "reject") {
                const note = window.prompt(t("approvals.rejectReasonPrompt"));
                if (note)
                    comment = note;
            }
            await api.approveNode(token, item.run_id, item.node_id, decision, comment);
            await refresh();
        }
        catch (e) {
            const err = e;
            if (err instanceof APIError && err.status === 409) {
                // Someone else (or an earlier click) already resumed it; just
                // refresh and move on.
                await refresh();
                return;
            }
            setError(e.message);
        }
        finally {
            setActing((s) => {
                const next = { ...s };
                delete next[key];
                return next;
            });
        }
    };
    return (_jsxs("div", { children: [_jsx("div", { className: "page-title", children: _jsxs("div", { children: [_jsx("h1", { children: t("approvals.title") }), _jsxs("div", { className: "sub", children: [t("approvals.subtitle", {
                                    tenant: activeTenant || me?.tenant,
                                    workspace: activeWorkspace || me?.workspace || t("approvals.anyWorkspace"),
                                }), items.length > 0 && (_jsxs(_Fragment, { children: [" · ", _jsx(Trans, { i18nKey: "approvals.waitingSuffix", values: { count: items.length }, components: [_jsx("strong", {})] })] }))] })] }) }), error && (_jsx("div", { className: "card", style: { color: "var(--danger)" }, children: error })), !error && loading && items.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: t("common.loading") })), !error && !loading && items.length === 0 && (_jsxs("div", { className: "card approvals-empty", children: [_jsx(Inbox, { size: 28, style: { opacity: 0.5, marginBottom: 8 } }), _jsx("div", { children: t("approvals.inboxZero") })] })), _jsx("div", { className: "approval-list", children: items.map((item) => {
                    const key = `${item.run_id}/${item.node_id}`;
                    const inflight = acting[key];
                    return (_jsx("div", { className: "approval-card", children: _jsxs("div", { className: "approval-head", children: [_jsxs("div", { style: { minWidth: 0, flex: 1 }, children: [_jsx("div", { className: "approval-prompt", children: item.prompt || t("approvals.noPrompt", { nodeId: item.node_id }) }), _jsxs("div", { className: "approval-meta", children: [_jsxs(Link, { to: `/flows/${encodeURIComponent(item.graph_id)}?run=${encodeURIComponent(item.run_id)}`, children: [_jsx(Workflow, { size: 11, style: { verticalAlign: -1 } }), " ", item.graph_id] }), _jsx("span", { children: "\u00B7" }), _jsx("span", { title: item.since, children: formatTime(item.since) }), _jsx("span", { children: "\u00B7" }), _jsx("span", { style: { fontFamily: "var(--font-mono)" }, children: item.node_id })] })] }), _jsxs("div", { className: "approval-actions", children: [_jsxs("button", { onClick: () => decide(item, "reject"), disabled: !!inflight, children: [_jsx(XCircle, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), inflight === "reject" ? t("approvals.rejecting") : t("approvals.reject")] }), _jsxs("button", { className: "primary", onClick: () => decide(item, "approve"), disabled: !!inflight, children: [_jsx(CheckCircle2, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), inflight === "approve" ? t("approvals.approving") : t("approvals.approve")] })] })] }) }, key));
                }) })] }));
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
