import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Workflow, Lock, Globe } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { iconFor, isBrandedIcon } from "../icons";
export function FlowList() {
    const { t } = useTranslation();
    const { token, me, activeTenant, activeWorkspace } = useAuth();
    const [flows, setFlows] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const navigate = useNavigate();
    useEffect(() => {
        if (!token || !me || !activeWorkspace)
            return;
        let cancelled = false;
        setLoading(true);
        api
            .listGraphs(token, activeTenant, activeWorkspace)
            .then((r) => {
            if (!cancelled)
                setFlows(r.graphs ?? []);
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
    }, [token, me, activeWorkspace]);
    const createNew = async () => {
        if (!token || !me || !activeWorkspace)
            return;
        const id = window.prompt(t("flowList.newFlowPrompt"));
        if (!id)
            return;
        await api.saveGraph(token, {
            id,
            tenant: activeTenant,
            workspace: activeWorkspace,
            nodes: [],
            edges: [],
        });
        navigate(`/flows/${encodeURIComponent(id)}`);
    };
    return (_jsxs("div", { children: [_jsxs("div", { className: "page-title", children: [_jsxs("div", { children: [_jsx("h1", { children: t("flowList.title") }), _jsxs("div", { className: "sub", children: [activeTenant || me?.tenant, "/", activeWorkspace] })] }), _jsxs("div", { style: { display: "flex", gap: 8 }, children: [_jsx(Link, { to: "/templates", style: { textDecoration: "none" }, className: "secondary-link", children: _jsx("button", { type: "button", className: "secondary", children: t("flowList.fromTemplate") }) }), _jsxs("button", { className: "primary", onClick: createNew, children: [_jsx(Plus, { size: 16, style: { marginRight: 6, verticalAlign: -3 } }), t("flowList.newFlow")] })] })] }), loading && _jsx("div", { className: "card", children: t("common.loading") }), error && _jsx("div", { className: "card", style: { color: "var(--danger)" }, children: error }), !loading && !error && flows.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: t("flowList.empty") })), _jsx("div", { className: "graph-list", children: flows.map((f) => {
                    const isPrivate = f.visibility === "private";
                    const ownedByMe = !!me && f.owner === me.subject;
                    const Icon = f.icon ? iconFor(f.icon) : Workflow;
                    const displayName = f.name || f.id;
                    return (_jsx(Link, { to: `/flows/${encodeURIComponent(f.id)}`, style: { textDecoration: "none", color: "inherit" }, children: _jsxs("div", { className: "graph-card", children: [_jsxs("div", { className: "name", children: [_jsx(Icon, { size: isBrandedIcon(f.icon) ? 20 : 16, color: isBrandedIcon(f.icon) ? undefined : "currentColor" }), _jsxs("span", { style: { flex: 1, minWidth: 0 }, children: [_jsx("span", { style: { display: "block" }, children: displayName }), f.name && (_jsx("span", { style: {
                                                        fontFamily: "var(--font-mono)",
                                                        fontSize: 11,
                                                        color: "var(--faint)",
                                                    }, children: f.id }))] }), isPrivate ? (_jsxs("span", { className: "vis-badge private", title: ownedByMe
                                                ? t("flowList.privateOwnedByYou")
                                                : t("flowList.privateOwnedBy", {
                                                    owner: f.owner ?? t("common.unknownParen"),
                                                }), children: [_jsx(Lock, { size: 11 }), t("common.private")] })) : (_jsxs("span", { className: "vis-badge org", title: t("flowList.orgTooltip"), children: [_jsx(Globe, { size: 11 }), t("common.org")] }))] }), f.description && (_jsx("div", { className: "meta", style: { color: "var(--muted)", lineHeight: 1.4 }, children: f.description })), _jsx("div", { className: "meta", children: f.owner && (_jsxs(_Fragment, { children: [t("flowList.ownerLabel"), " ", _jsx("code", { children: f.owner })] })) })] }) }, f.id));
                }) })] }));
}
