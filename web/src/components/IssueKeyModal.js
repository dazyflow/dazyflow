import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
export const ROLE_TEMPLATES = [
    {
        id: "viewer",
        nameKey: "issueKey.templateViewer",
        descKey: "issueKey.templateViewerDesc",
        permissions: ["graph:run"],
    },
    {
        id: "operator",
        nameKey: "issueKey.templateOperator",
        descKey: "issueKey.templateOperatorDesc",
        permissions: ["graph:run", "graph:edit", "secret:read"],
    },
    {
        id: "admin",
        nameKey: "issueKey.templateAdmin",
        descKey: "issueKey.templateAdminDesc",
        permissions: [
            "graph:run",
            "graph:edit",
            "graph:admin",
            "secret:read",
            "secret:write",
            "tenant:admin",
        ],
    },
];
export const ALL_PERMISSIONS = [
    "graph:run",
    "graph:edit",
    "graph:admin",
    "module:register",
    "secret:read",
    "secret:write",
    "tenant:admin",
];
export function IssueKeyModal({ initialSubject, onCancel, onIssued, onError, }) {
    const { t } = useTranslation();
    const { token, activeTenant } = useAuth();
    const [subject, setSubject] = useState(initialSubject ?? "");
    const [templateID, setTemplateID] = useState("custom");
    const [roleName, setRoleName] = useState("custom");
    const [perms, setPerms] = useState(new Set(["graph:run"]));
    const [submitting, setSubmitting] = useState(false);
    const applyTemplate = (id) => {
        setTemplateID(id);
        if (id === "custom")
            return;
        const t = ROLE_TEMPLATES.find((x) => x.id === id);
        if (!t)
            return;
        setPerms(new Set(t.permissions));
        setRoleName(t.id);
    };
    const togglePerm = (p) => setPerms((s) => {
        const next = new Set(s);
        if (next.has(p))
            next.delete(p);
        else
            next.add(p);
        // Selecting permissions manually pops us off the active template
        // so we don't pretend a custom set is one of the canned roles.
        setTemplateID("custom");
        return next;
    });
    const submit = async (e) => {
        e.preventDefault();
        if (!token)
            return;
        if (!subject.trim()) {
            onError(t("issueKey.subjectRequired"));
            return;
        }
        if (perms.size === 0) {
            onError(t("issueKey.needAtLeastOnePerm"));
            return;
        }
        setSubmitting(true);
        try {
            const role = {
                name: roleName || "custom",
                permissions: Array.from(perms),
            };
            const issued = await api.issueAPIKey(token, {
                subject: subject.trim(),
                // Platform admins working in a switched tenant should issue
                // there, not in their (often empty) own tenant. The backend
                // refuses cross-tenant issuance for non-platform-admins, so
                // sending activeTenant is safe for everyone.
                tenant: activeTenant || undefined,
                roles: [role],
            });
            onIssued(issued);
        }
        catch (e) {
            onError(e.message);
        }
        finally {
            setSubmitting(false);
        }
    };
    return (_jsx("div", { className: "settings-backdrop", onClick: onCancel, children: _jsxs("form", { className: "settings-dialog", style: { maxWidth: 560 }, onClick: (e) => e.stopPropagation(), onSubmit: submit, children: [_jsx("div", { className: "settings-head", children: _jsx("h2", { children: t("issueKey.title") }) }), _jsxs("div", { className: "settings-body", children: [_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("issueKey.subjectLabel") }) }), _jsx("input", { autoFocus: !initialSubject, value: subject, onChange: (e) => setSubject(e.target.value), placeholder: t("issueKey.subjectPlaceholder") })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("issueKey.roleTemplate") }) }), _jsxs("div", { className: "role-template-grid", children: [ROLE_TEMPLATES.map((tpl) => (_jsxs("button", { type: "button", className: "role-template" + (templateID === tpl.id ? " active" : ""), onClick: () => applyTemplate(tpl.id), children: [_jsx("div", { className: "role-template-name", children: t(tpl.nameKey) }), _jsx("div", { className: "role-template-desc", children: t(tpl.descKey) })] }, tpl.id))), _jsxs("button", { type: "button", className: "role-template" + (templateID === "custom" ? " active" : ""), onClick: () => applyTemplate("custom"), children: [_jsx("div", { className: "role-template-name", children: t("issueKey.templateCustom") }), _jsx("div", { className: "role-template-desc", children: t("issueKey.templateCustomDesc") })] })] })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("issueKey.roleName") }) }), _jsx("input", { value: roleName, onChange: (e) => setRoleName(e.target.value), placeholder: t("issueKey.rolePlaceholder") }), _jsx("div", { className: "desc", children: t("issueKey.roleNameDesc") })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("issueKey.permissions") }) }), _jsx("div", { className: "perm-grid", children: ALL_PERMISSIONS.map((p) => (_jsxs("label", { className: "sf-checkbox", children: [_jsx("input", { type: "checkbox", checked: perms.has(p), onChange: () => togglePerm(p) }), _jsx("span", { style: { fontFamily: "var(--font-mono)", fontSize: 11 }, children: p })] }, p))) }), _jsx("div", { className: "desc", children: _jsx(Trans, { i18nKey: "issueKey.tenantAdminWarning", components: [_jsx("strong", {})] }) })] })] }), _jsxs("div", { className: "settings-foot", children: [_jsx("button", { type: "button", onClick: onCancel, children: t("issueKey.cancel") }), _jsx("button", { type: "submit", className: "primary", disabled: submitting, children: submitting ? t("issueKey.issuing") : t("issueKey.issue") })] })] }) }));
}
