import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { KeyRound, Users, Settings2, Boxes, ShieldAlert } from "lucide-react";
import { useAuth } from "../auth";
// Admin is the gating point for tenant-level configuration. The
// concrete management UIs (API keys, users, workspace settings) are
// stubs today — they need matching backend endpoints (see TODO).
export function Admin() {
    const { me, hasPerm } = useAuth();
    if (!hasPerm("tenant:admin") && !hasPerm("graph:admin")) {
        return (_jsxs("div", { className: "card", style: { color: "var(--danger)" }, children: ["You need ", _jsx("code", { children: "tenant:admin" }), " or ", _jsx("code", { children: "graph:admin" }), " to view this page."] }));
    }
    return (_jsxs("div", { children: [_jsx("div", { className: "page-title", children: _jsxs("div", { children: [_jsx("h1", { children: "Admin" }), _jsxs("div", { className: "sub", children: ["Tenant ", _jsx("strong", { children: me?.tenant }), " \u00B7 workspace", " ", _jsx("strong", { children: me?.workspace })] })] }) }), _jsxs("div", { className: "admin-grid", children: [_jsx(AdminCard, { icon: _jsx(KeyRound, { size: 16 }), title: "API keys", desc: "Issue, list, and revoke API keys. UI stub \u2014 wires up once the daemon exposes /api/v1/admin/api-keys.", status: "stub" }), _jsx(AdminCard, { icon: _jsx(Users, { size: 16 }), title: "Users & roles", desc: "Map principals to roles. The role system already exists in core; the UI needs a roles-CRUD endpoint.", status: "stub" }), _jsx(AdminCard, { icon: _jsx(Settings2, { size: 16 }), title: "Workspace settings", desc: "Quotas, sandbox roots, retention. Reads/writes to the daemon's tenant config.", status: "stub" }), _jsx(AdminCard, { icon: _jsx(Boxes, { size: 16 }), title: "Module registry", desc: "Inspect installed modules and (later) approve remote/MCP modules.", status: "stub" }), _jsx(AdminCard, { icon: _jsx(ShieldAlert, { size: 16 }), title: "Audit log", desc: "View graph saves, runs, secret accesses, approval decisions.", status: "stub" })] })] }));
}
function AdminCard({ icon, title, desc, status, }) {
    return (_jsxs("div", { className: "admin-card", children: [_jsxs("h3", { children: [icon, title] }), _jsx("div", { className: "desc", children: desc }), _jsx("span", { className: "badge", children: status === "stub" ? "Stub" : "Ready" })] }));
}
