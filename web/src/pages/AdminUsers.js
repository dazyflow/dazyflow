import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { AlertCircle, KeyRound, Plus, Users, UserCircle2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import { IssueKeyModal } from "../components/IssueKeyModal";
import { RevealSecretModal } from "../components/RevealSecretModal";
// AdminUsers groups API keys by Subject — Hazy Flow doesn't have a
// separate users table, so the "user" is derived from the keys' Subject
// field. Permissions are the union of permissions across the user's
// active keys, which matches what they'd effectively get if they used
// all their keys at once.
//
// "Issue another key" prefills the subject so common-case admin work
// (rotation, multi-device key) is one fewer click.
export function AdminUsers() {
    const { t } = useTranslation();
    const { token, hasPerm, activeTenant } = useAuth();
    const [users, setUsers] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [creating, setCreating] = useState(null); // subject prefill, null = new
    const [revealed, setRevealed] = useState(null);
    const refresh = useCallback(async () => {
        if (!token)
            return;
        setLoading(true);
        try {
            const r = await api.listUsers(token, activeTenant || undefined);
            setUsers(r.users ?? []);
            setError(null);
        }
        catch (e) {
            const err = e;
            if (err instanceof APIError && err.status === 501) {
                setError(t("admin.users.notConfigured"));
            }
            else {
                setError(err.message);
            }
        }
        finally {
            setLoading(false);
        }
    }, [token, activeTenant]);
    useEffect(() => {
        void refresh();
    }, [refresh]);
    if (!hasPerm("tenant:admin")) {
        return (_jsx("div", { className: "card", style: { color: "var(--danger)" }, children: _jsx(Trans, { i18nKey: "admin.users.needAdmin", components: [_jsx("code", {})] }) }));
    }
    return (_jsxs("div", { children: [_jsxs("div", { className: "page-title", children: [_jsxs("div", { children: [_jsxs("h1", { children: [_jsx(Users, { size: 20, style: { marginRight: 8, verticalAlign: -3 } }), t("admin.users.title")] }), _jsx("div", { className: "sub", children: t("admin.users.subtitle") })] }), _jsxs("button", { className: "primary", onClick: () => setCreating(""), children: [_jsx(Plus, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), t("admin.users.addUser")] })] }), error && (_jsxs("div", { className: "card", style: { color: "var(--danger)", marginBottom: "var(--space-4)" }, children: [_jsx(AlertCircle, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), error] })), loading && users.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: t("common.loading") })), !loading && users.length === 0 && !error && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: t("admin.users.empty") })), _jsx("div", { className: "user-list", children: users.map((u) => (_jsxs("div", { className: "user-card", children: [_jsxs("div", { style: { minWidth: 0 }, children: [_jsxs("div", { className: "subject", children: [_jsx(UserCircle2, { size: 18 }), u.subject] }), _jsxs("div", { className: "meta", children: [u.role_names.length > 0
                                            ? u.role_names.join(", ")
                                            : t("admin.users.noRoles"), u.last_workspace && (_jsxs(_Fragment, { children: [" \u00B7 ", t("admin.users.workspaceLabel"), " ", _jsx("code", { children: u.last_workspace })] }))] }), _jsxs("div", { className: "count-pills", children: [_jsx("span", { className: "count-pill active", children: t("admin.users.activePill", { count: u.active_keys }) }), u.revoked_keys > 0 && (_jsx("span", { className: "count-pill revoked", children: t("admin.users.revokedPill", { count: u.revoked_keys }) }))] }), u.permissions.length > 0 && (_jsx("div", { className: "perm-row", children: u.permissions.map((p) => (_jsx("span", { className: "perm-chip" + (p === "tenant:admin" ? " admin" : ""), children: p }, p))) }))] }), _jsxs("div", { className: "user-card-actions", children: [_jsxs(Link, { to: "/admin/api-keys", className: "ghost", style: {
                                        display: "inline-flex",
                                        alignItems: "center",
                                        gap: 4,
                                        fontSize: 12,
                                        padding: "4px 10px",
                                        border: "1px solid var(--border)",
                                        borderRadius: "var(--r-2)",
                                        color: "var(--muted)",
                                        textDecoration: "none",
                                    }, children: [_jsx(KeyRound, { size: 12 }), t("admin.users.keys", { count: u.key_ids.length })] }), _jsxs("button", { onClick: () => setCreating(u.subject), title: t("admin.users.issueKeyTitle"), children: [_jsx(Plus, { size: 12, style: { marginRight: 4, verticalAlign: -1 } }), t("admin.users.issueKey")] })] })] }, u.subject))) }), creating !== null && (_jsx(IssueKeyModal, { initialSubject: creating || undefined, onCancel: () => setCreating(null), onIssued: (issued) => {
                    setCreating(null);
                    setRevealed(issued);
                    void refresh();
                }, onError: (msg) => setError(msg) })), revealed && (_jsx(RevealSecretModal, { issued: revealed, onClose: () => setRevealed(null) }))] }));
}
