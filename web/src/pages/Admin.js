import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { Link } from "react-router-dom";
import { KeyRound, Users, Settings2, Boxes, ShieldAlert } from "lucide-react";
import { useAuth } from "../auth";
// Admin is the gating point for tenant-level configuration. Each card
// links to a focused sub-page when the underlying API + UI exists, and
// stays as a stub otherwise. The role gate accepts either tenant:admin
// (the right one) or graph:admin (a coarser fallback so power users
// who set the system up can land here even before refining roles).
export function Admin() {
    const { me, hasPerm, activeTenant, activeWorkspace } = useAuth();
    if (!hasPerm("tenant:admin") && !hasPerm("graph:admin")) {
        return (_jsxs("div", { className: "card", style: { color: "var(--danger)" }, children: ["You need ", _jsx("code", { children: "tenant:admin" }), " or ", _jsx("code", { children: "graph:admin" }), " to view this page."] }));
    }
    return (_jsxs("div", { children: [_jsx("div", { className: "page-title", children: _jsxs("div", { children: [_jsx("h1", { children: "Admin" }), _jsxs("div", { className: "sub", children: ["Tenant ", _jsx("strong", { children: activeTenant || me?.tenant }), " \u00B7 workspace", " ", _jsx("strong", { children: activeWorkspace || me?.workspace || "(any)" })] })] }) }), _jsxs("div", { className: "admin-grid", children: [_jsx(AdminCard, { to: "/admin/api-keys", icon: _jsx(KeyRound, { size: 16 }), title: "API keys", desc: "Issue, list, and revoke bearer tokens for this tenant.", status: "ready" }), _jsx(AdminCard, { to: "/admin/users", icon: _jsx(Users, { size: 16 }), title: "Users & roles", desc: "Subjects derived from API keys, grouped with their effective permissions.", status: "ready" }), _jsx(AdminCard, { icon: _jsx(Settings2, { size: 16 }), title: "Workspace settings", desc: "Quotas, sandbox roots, retention. Reads/writes to the daemon's tenant config.", status: "stub" }), _jsx(AdminCard, { icon: _jsx(Boxes, { size: 16 }), title: "Module registry", desc: "Inspect installed modules and (later) approve remote/MCP modules.", status: "stub" }), _jsx(AdminCard, { icon: _jsx(ShieldAlert, { size: 16 }), title: "Audit log", desc: "Graph saves, runs, secret accesses, approval decisions \u2014 needs persistence + instrumentation.", status: "stub" })] })] }));
}
function AdminCard({ icon, title, desc, status, to, }) {
    const body = (_jsxs("div", { className: "admin-card", children: [_jsxs("h3", { children: [icon, title] }), _jsx("div", { className: "desc", children: desc }), _jsx("span", { className: "badge", children: status === "stub" ? "Stub" : "Ready" })] }));
    return to ? (_jsx(Link, { to: to, style: { textDecoration: "none", color: "inherit" }, children: body })) : (body);
}
