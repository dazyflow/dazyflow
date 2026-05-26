import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useCallback, useEffect, useState } from "react";
import { KeyRound, Plus, Trash2, AlertCircle } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import { IssueKeyModal } from "../components/IssueKeyModal";
import { RevealSecretModal } from "../components/RevealSecretModal";
export function AdminAPIKeys() {
    const { t } = useTranslation();
    const { token, hasPerm, activeTenant } = useAuth();
    const [keys, setKeys] = useState([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState(null);
    const [creating, setCreating] = useState(false);
    // Holds the just-minted key so the UI can show the secret once.
    // Clearing it (close) is one-way; the secret is never recoverable.
    const [revealed, setRevealed] = useState(null);
    const refresh = useCallback(async () => {
        if (!token)
            return;
        setLoading(true);
        try {
            const r = await api.listAPIKeys(token, activeTenant || undefined);
            setKeys(r.keys ?? []);
            setError(null);
        }
        catch (e) {
            const err = e;
            if (err instanceof APIError && err.status === 501) {
                setError(t("admin.apiKeys.notConfigured"));
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
        return (_jsx("div", { className: "card", style: { color: "var(--danger)" }, children: _jsx(Trans, { i18nKey: "admin.apiKeys.needAdmin", components: [_jsx("code", {})] }) }));
    }
    const revoke = async (id) => {
        if (!token)
            return;
        if (!window.confirm(t("admin.apiKeys.confirmRevoke", { id }))) {
            return;
        }
        try {
            await api.revokeAPIKey(token, id);
            await refresh();
        }
        catch (e) {
            setError(e.message);
        }
    };
    return (_jsxs("div", { children: [_jsxs("div", { className: "page-title", children: [_jsxs("div", { children: [_jsxs("h1", { children: [_jsx(KeyRound, { size: 20, style: { marginRight: 8, verticalAlign: -3 } }), t("admin.apiKeys.title")] }), _jsx("div", { className: "sub", children: t("admin.apiKeys.subtitle") })] }), _jsxs("button", { className: "primary", onClick: () => setCreating(true), children: [_jsx(Plus, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), t("admin.apiKeys.issueKey")] })] }), error && (_jsxs("div", { className: "card", style: { color: "var(--danger)", marginBottom: "var(--space-4)" }, children: [_jsx(AlertCircle, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), error] })), loading && keys.length === 0 && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: t("common.loading") })), !loading && keys.length === 0 && !error && (_jsx("div", { className: "card", style: { color: "var(--muted)" }, children: t("admin.apiKeys.empty") })), keys.length > 0 && (_jsx("div", { className: "card", style: { padding: 0, overflow: "hidden" }, children: _jsxs("table", { className: "run-table", children: [_jsx("thead", { children: _jsxs("tr", { children: [_jsx("th", { children: t("admin.apiKeys.colId") }), _jsx("th", { children: t("admin.apiKeys.colSubject") }), _jsx("th", { children: t("admin.apiKeys.colWorkspace") }), _jsx("th", { children: t("admin.apiKeys.colRoles") }), _jsx("th", { children: t("admin.apiKeys.colStatus") }), _jsx("th", {})] }) }), _jsx("tbody", { children: keys.map((k) => (_jsxs("tr", { children: [_jsx("td", { style: { fontFamily: "var(--font-mono)", fontSize: 12 }, children: k.id }), _jsx("td", { children: k.subject }), _jsx("td", { style: { color: "var(--muted)", fontSize: 12 }, children: k.workspace || t("admin.apiKeys.anyWorkspace") }), _jsx("td", { style: { fontSize: 12 }, children: k.roles.map((r) => r.name).join(", ") }), _jsx("td", { children: _jsx("span", { className: `key-status ${k.status}`, children: k.status }) }), _jsx("td", { style: { textAlign: "right" }, children: k.status === "active" && (_jsx("button", { className: "ghost", onClick: () => revoke(k.id), title: t("admin.apiKeys.revokeTitle"), children: _jsx(Trash2, { size: 14 }) })) })] }, k.id))) })] }) })), creating && (_jsx(IssueKeyModal, { onCancel: () => setCreating(false), onIssued: (issued) => {
                    setCreating(false);
                    setRevealed(issued);
                    void refresh();
                }, onError: (msg) => setError(msg) })), revealed && (_jsx(RevealSecretModal, { issued: revealed, onClose: () => setRevealed(null) }))] }));
}
