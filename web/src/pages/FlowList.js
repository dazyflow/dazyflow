import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Workflow, Lock, Globe } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
export function FlowList() {
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
        const id = window.prompt("New flow ID:");
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
    return (_jsxs("div", { children: [_jsxs("div", { className: "page-title", children: [_jsxs("div", { children: [_jsx("h1", { children: "Flows" }), _jsxs("div", { className: "sub", children: [activeTenant || me?.tenant, "/", activeWorkspace] })] }), _jsxs("button", { className: "primary", onClick: createNew, children: [_jsx(Plus, { size: 16, style: { marginRight: 6, verticalAlign: -3 } }), "New flow"] })] }), loading && _jsx("div", { className: "card", children: "Loading\u2026" }), error && _jsx("div", { className: "card", style: { color: "var(--danger)" }, children: error }), !loading && !error && flows.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: "No flows yet. Create one to get started." })), _jsx("div", { className: "graph-list", children: flows.map((f) => {
                    const isPrivate = f.visibility === "private";
                    const ownedByMe = !!me && f.owner === me.subject;
                    return (_jsx(Link, { to: `/flows/${encodeURIComponent(f.id)}`, style: { textDecoration: "none", color: "inherit" }, children: _jsxs("div", { className: "graph-card", children: [_jsxs("div", { className: "name", children: [_jsx(Workflow, { size: 16 }), _jsx("span", { style: { flex: 1, minWidth: 0 }, children: f.id }), isPrivate ? (_jsxs("span", { className: "vis-badge private", title: ownedByMe
                                                ? "Private — only you can see this flow"
                                                : `Private — owned by ${f.owner ?? "(unknown)"}`, children: [_jsx(Lock, { size: 11 }), "Private"] })) : (_jsxs("span", { className: "vis-badge org", title: "Visible to everyone in this workspace", children: [_jsx(Globe, { size: 11 }), "Org"] }))] }), _jsx("div", { className: "meta", children: f.owner && (_jsxs(_Fragment, { children: ["Owner: ", _jsx("code", { children: f.owner })] })) })] }) }, f.id));
                }) })] }));
}
