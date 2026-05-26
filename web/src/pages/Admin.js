import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { Link } from "react-router-dom";
import { KeyRound, Users, Settings2, Boxes, ShieldAlert } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
// Admin is the gating point for tenant-level configuration. Each card
// links to a focused sub-page when the underlying API + UI exists, and
// stays as a stub otherwise. The role gate accepts either tenant:admin
// (the right one) or graph:admin (a coarser fallback so power users
// who set the system up can land here even before refining roles).
export function Admin() {
    const { t } = useTranslation();
    const { me, hasPerm, activeTenant, activeWorkspace } = useAuth();
    if (!hasPerm("tenant:admin") && !hasPerm("graph:admin")) {
        return (_jsx("div", { className: "card", style: { color: "var(--danger)" }, children: _jsx(Trans, { i18nKey: "admin.needAdmin", components: [_jsx("code", {})] }) }));
    }
    return (_jsxs("div", { children: [_jsx("div", { className: "page-title", children: _jsxs("div", { children: [_jsx("h1", { children: t("admin.title") }), _jsx("div", { className: "sub", children: _jsx(Trans, { i18nKey: "admin.subtitle", values: {
                                    tenant: activeTenant || me?.tenant,
                                    workspace: activeWorkspace || me?.workspace || t("admin.anyWorkspace"),
                                }, components: [_jsx("strong", {}), _jsx("strong", {})] }) })] }) }), _jsxs("div", { className: "admin-grid", children: [_jsx(AdminCard, { to: "/admin/api-keys", icon: _jsx(KeyRound, { size: 16 }), title: t("admin.cardApiKeysTitle"), desc: t("admin.cardApiKeysDesc"), status: "ready" }), _jsx(AdminCard, { to: "/admin/users", icon: _jsx(Users, { size: 16 }), title: t("admin.cardUsersTitle"), desc: t("admin.cardUsersDesc"), status: "ready" }), _jsx(AdminCard, { icon: _jsx(Settings2, { size: 16 }), title: t("admin.cardWorkspaceTitle"), desc: t("admin.cardWorkspaceDesc"), status: "stub" }), _jsx(AdminCard, { icon: _jsx(Boxes, { size: 16 }), title: t("admin.cardModulesTitle"), desc: t("admin.cardModulesDesc"), status: "stub" }), _jsx(AdminCard, { icon: _jsx(ShieldAlert, { size: 16 }), title: t("admin.cardAuditTitle"), desc: t("admin.cardAuditDesc"), status: "stub" })] })] }));
}
function AdminCard({ icon, title, desc, status, to, }) {
    const { t } = useTranslation();
    const body = (_jsxs("div", { className: "admin-card", children: [_jsxs("h3", { children: [icon, title] }), _jsx("div", { className: "desc", children: desc }), _jsx("span", { className: "badge", children: status === "stub" ? t("admin.statusStub") : t("admin.statusReady") })] }));
    return to ? (_jsx(Link, { to: to, style: { textDecoration: "none", color: "inherit" }, children: body })) : (body);
}
