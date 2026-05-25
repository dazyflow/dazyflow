import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Workflow } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
export function PipelineList() {
    const { token, me } = useAuth();
    const [graphs, setGraphs] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const navigate = useNavigate();
    useEffect(() => {
        if (!token || !me)
            return;
        let cancelled = false;
        api
            .listGraphs(token, me.tenant, me.workspace)
            .then((r) => {
            if (!cancelled)
                setGraphs(r.graphs ?? []);
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
    }, [token, me]);
    const createNew = async () => {
        if (!token || !me)
            return;
        const id = window.prompt("New pipeline ID:");
        if (!id)
            return;
        await api.saveGraph(token, {
            id,
            tenant: me.tenant,
            workspace: me.workspace,
            nodes: [],
            edges: [],
        });
        navigate(`/pipelines/${encodeURIComponent(id)}`);
    };
    return (_jsxs("div", { children: [_jsxs("div", { className: "page-title", children: [_jsxs("div", { children: [_jsx("h1", { children: "Pipelines" }), _jsxs("div", { className: "sub", children: [me?.tenant, "/", me?.workspace] })] }), _jsxs("button", { className: "primary", onClick: createNew, children: [_jsx(Plus, { size: 16, style: { marginRight: 6, verticalAlign: -3 } }), "New pipeline"] })] }), loading && _jsx("div", { className: "card", children: "Loading\u2026" }), error && _jsx("div", { className: "card", style: { color: "var(--danger)" }, children: error }), !loading && !error && graphs.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: "No pipelines yet. Create one to get started." })), _jsx("div", { className: "graph-list", children: graphs.map((id) => (_jsx(Link, { to: `/pipelines/${encodeURIComponent(id)}`, style: { textDecoration: "none", color: "inherit" }, children: _jsxs("div", { className: "graph-card", children: [_jsxs("div", { className: "name", children: [_jsx(Workflow, { size: 16 }), id] }), _jsx("div", { className: "meta", children: "Click to edit" })] }) }, id))) })] }));
}
