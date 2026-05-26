import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { Menu, LogOut, Workflow, ShieldCheck, Activity, Inbox, ChevronDown, FolderTree, Building2, Boxes, } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { useAuth } from "../auth";
export function AppShell({ children }) {
    const { t } = useTranslation();
    const { token, me, signOut, hasPerm, workspaces, activeWorkspace, setActiveWorkspace, tenants, activeTenant, setActiveTenant, } = useAuth();
    const [navOpen, setNavOpen] = useState(false);
    const location = useLocation();
    // Pending approvals count — surfaces a badge on the sidebar nav so
    // operators see "you have N decisions waiting" without visiting the
    // page. Polled every 30s; updates immediately on visibility change.
    const [pendingCount, setPendingCount] = useState(0);
    useEffect(() => {
        if (!token)
            return;
        let cancelled = false;
        const fetch = () => api
            // Match the inbox's workspace narrow so the badge count
            // doesn't disagree with what the user sees when they click it.
            .listPendingApprovals(token, {
            workspace: activeWorkspace || undefined,
            tenant: activeTenant || undefined,
        })
            .then((r) => {
            if (!cancelled)
                setPendingCount(r.approvals?.length ?? 0);
        })
            .catch(() => {
            /* ignore — non-essential */
        });
        void fetch();
        const t = window.setInterval(fetch, 30000);
        return () => {
            cancelled = true;
            window.clearInterval(t);
        };
    }, [token, location.pathname, activeTenant, activeWorkspace]);
    // Editor pages need a full-bleed canvas — remove the main padding.
    // Editor pages need a full-bleed canvas. Match either the canonical
    // /flows/:id or the legacy /pipelines/:id path so an incoming legacy
    // link still gets the right layout during the one-render redirect.
    const inEditor = /^\/(flows|pipelines)\/[^/]+/.test(location.pathname);
    const showAdmin = hasPerm("tenant:admin") || hasPerm("graph:admin");
    return (_jsxs("div", { className: "app-shell", children: [_jsxs("header", { className: "topbar", children: [_jsx("button", { className: "icon ghost hamburger", onClick: () => setNavOpen((x) => !x), "aria-label": t("nav.toggleNav"), children: _jsx(Menu, { size: 20 }) }), _jsxs("div", { className: "brand", children: [_jsx("span", { className: "brand-mark", children: "\u223C" }), _jsx("span", { children: "Hazy Flow" })] }), _jsx("div", { className: "spacer" }), me && (_jsxs("div", { className: "user", children: [tenants.length > 1 && (_jsx(TenantSwitcher, { tenants: tenants, activeTenant: activeTenant || me.tenant, onPick: setActiveTenant })), _jsx(WorkspaceSwitcher, { tenant: activeTenant || me.tenant, activeWorkspace: activeWorkspace || me.workspace, workspaces: workspaces, onPick: setActiveWorkspace, hideTenantPrefix: tenants.length > 1 }), _jsx("span", { style: { color: "var(--faint)" }, children: "\u00B7" }), _jsx("span", { className: "who", children: me.subject || t("nav.noSubject") }), _jsx("button", { className: "icon ghost", onClick: signOut, "aria-label": t("nav.signOut"), children: _jsx(LogOut, { size: 18 }) })] }))] }), _jsxs("div", { className: "body", children: [navOpen && (_jsx("div", { className: "sidebar-backdrop", onClick: () => setNavOpen(false) })), _jsxs("aside", { className: "sidebar", "data-open": navOpen ? "true" : "false", children: [_jsx("div", { className: "group-label", children: t("nav.workspaceGroup") }), _jsxs(NavLink, { to: "/flows", onClick: () => setNavOpen(false), children: [_jsx(Workflow, { size: 18 }), t("nav.flows")] }), _jsxs(NavLink, { to: "/runs", onClick: () => setNavOpen(false), children: [_jsx(Activity, { size: 18 }), t("nav.runs")] }), _jsxs(NavLink, { to: "/approvals", onClick: () => setNavOpen(false), children: [_jsx(Inbox, { size: 18 }), _jsx("span", { style: { flex: 1 }, children: t("nav.approvals") }), pendingCount > 0 && (_jsx("span", { className: "nav-badge", children: pendingCount }))] }), _jsxs(NavLink, { to: "/integrations", onClick: () => setNavOpen(false), children: [_jsx(Boxes, { size: 18 }), t("nav.integrations")] }), showAdmin && (_jsxs(_Fragment, { children: [_jsx("div", { className: "group-label", children: t("nav.settingsGroup") }), _jsxs(NavLink, { to: "/admin", onClick: () => setNavOpen(false), children: [_jsx(ShieldCheck, { size: 18 }), t("nav.admin")] })] }))] }), _jsx("main", { className: "main" + (inEditor ? " no-pad" : ""), children: children })] })] }));
}
// WorkspaceSwitcher renders the tenant/workspace chip. When the
// principal can access more than one workspace, the chip becomes a
// dropdown that lets them pick the active one. Single-workspace
// principals see a flat label.
//
// hideTenantPrefix drops the "tenant/" prefix when a separate tenant
// switcher is already visible — avoids the redundant repetition that
// would otherwise show up for platform admins.
function WorkspaceSwitcher({ tenant, activeWorkspace, workspaces, onPick, hideTenantPrefix, }) {
    const { t } = useTranslation();
    const [open, setOpen] = useState(false);
    const multi = workspaces.length > 1;
    useEffect(() => {
        if (!open)
            return;
        const onDown = (e) => {
            const target = e.target;
            if (!target.closest(".workspace-switcher"))
                setOpen(false);
        };
        document.addEventListener("mousedown", onDown);
        return () => document.removeEventListener("mousedown", onDown);
    }, [open]);
    const label = hideTenantPrefix ? (_jsx("strong", { children: activeWorkspace || t("common.noneParen") })) : (_jsxs(_Fragment, { children: [tenant, "/", _jsx("strong", { children: activeWorkspace || t("common.noneParen") })] }));
    if (!multi) {
        return (_jsx("span", { style: { fontSize: 13, color: "var(--muted)" }, children: label }));
    }
    return (_jsxs("div", { className: "workspace-switcher", style: { position: "relative" }, children: [_jsxs("button", { type: "button", className: "ghost", onClick: () => setOpen((v) => !v), style: {
                    display: "inline-flex",
                    alignItems: "center",
                    gap: 6,
                    fontSize: 13,
                    padding: "4px 10px",
                }, title: t("nav.switchWorkspace"), children: [_jsx(FolderTree, { size: 13 }), _jsx("span", { children: label }), _jsx(ChevronDown, { size: 12 })] }), open && (_jsxs("div", { className: "workspace-pop", children: [_jsx("div", { className: "workspace-pop-head", children: tenant }), workspaces.map((ws) => (_jsx("button", { type: "button", className: "workspace-pop-row" + (ws === activeWorkspace ? " active" : ""), onClick: () => {
                            onPick(ws);
                            setOpen(false);
                        }, children: ws }, ws)))] }))] }));
}
// TenantSwitcher is the cross-tenant picker shown to platform admins.
// Picking a tenant clears the workspace selection (workspaces are
// tenant-scoped; the post-update whoami pass repopulates).
function TenantSwitcher({ tenants, activeTenant, onPick, }) {
    const { t: tr } = useTranslation();
    const [open, setOpen] = useState(false);
    useEffect(() => {
        if (!open)
            return;
        const onDown = (e) => {
            const target = e.target;
            if (!target.closest(".tenant-switcher"))
                setOpen(false);
        };
        document.addEventListener("mousedown", onDown);
        return () => document.removeEventListener("mousedown", onDown);
    }, [open]);
    return (_jsxs("div", { className: "tenant-switcher", style: { position: "relative" }, children: [_jsxs("button", { type: "button", className: "ghost", onClick: () => setOpen((v) => !v), style: {
                    display: "inline-flex",
                    alignItems: "center",
                    gap: 6,
                    fontSize: 13,
                    padding: "4px 10px",
                }, title: tr("nav.switchTenant"), children: [_jsx(Building2, { size: 13 }), _jsx("strong", { children: activeTenant || tr("nav.pickTenant") }), _jsx(ChevronDown, { size: 12 })] }), open && (_jsxs("div", { className: "workspace-pop", children: [_jsx("div", { className: "workspace-pop-head", children: tr("nav.tenants") }), tenants.map((t) => (_jsx("button", { type: "button", className: "workspace-pop-row" + (t === activeTenant ? " active" : ""), onClick: () => {
                            onPick(t);
                            setOpen(false);
                        }, children: t }, t)))] }))] }));
}
