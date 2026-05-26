import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { iconFor } from "../icons";
// Templates is the gallery page: lists pre-built workflows the user
// can fork into their own workspace with one click. On click we
// fetch the template's graph file, generate a fresh graph ID, fill
// in the user's tenant + workspace, and PUT through the normal
// saveGraph endpoint — same code path as creating a graph by hand,
// just pre-populated with nodes + edges.
//
// The gallery itself is static (web/public/templates/index.json).
// Adding a template is a JSON file + a one-line index entry; no
// daemon code change.
export function Templates() {
    const { t } = useTranslation();
    const { token, activeTenant, activeWorkspace } = useAuth();
    const navigate = useNavigate();
    const [templates, setTemplates] = useState(null);
    const [error, setError] = useState(null);
    const [busy, setBusy] = useState(null); // template id currently being forked
    useEffect(() => {
        api
            .listTemplates()
            .then((r) => setTemplates(r.templates))
            .catch((e) => setError(e.message));
    }, []);
    const useTemplate = async (tpl) => {
        if (!token || !activeTenant || !activeWorkspace) {
            setError(t("templates.notSignedIn"));
            return;
        }
        setBusy(tpl.id);
        setError(null);
        try {
            const tplGraph = await api.loadTemplateGraph(tpl.graph_file);
            // Generate a fresh ID — keep a human-readable slug from the
            // template ID plus a short suffix so multiple forks of the
            // same template don't collide.
            const suffix = Math.random().toString(36).slice(2, 8);
            const newID = `${tpl.id}-${suffix}`;
            const cloned = {
                ...tplGraph,
                id: newID,
                tenant: activeTenant,
                workspace: activeWorkspace,
                // owner intentionally left blank — the daemon stamps the
                // caller as owner on first save.
                owner: "",
            };
            await api.saveGraph(token, cloned);
            navigate(`/flows/${encodeURIComponent(newID)}`);
        }
        catch (e) {
            const msg = e instanceof APIError ? `${e.status}: ${e.message}` : e.message;
            setError(t("templates.forkFailed", { title: tpl.title, error: msg }));
        }
        finally {
            setBusy(null);
        }
    };
    if (error && !templates) {
        return (_jsxs("div", { className: "page", children: [_jsx("h1", { children: t("templates.title") }), _jsx("div", { className: "card error", children: error })] }));
    }
    if (!templates) {
        return (_jsxs("div", { className: "page", children: [_jsx("h1", { children: t("templates.title") }), _jsx("div", { className: "card", children: t("common.loading") })] }));
    }
    return (_jsxs("div", { className: "page templates-page", children: [_jsx("h1", { children: t("templates.title") }), _jsx("p", { className: "page-sub", children: t("templates.intro") }), error && _jsx("div", { className: "card error", style: { marginBottom: 12 }, children: error }), _jsx("div", { className: "template-grid", children: templates.map((tpl) => {
                    const Icon = iconFor(tpl.icon);
                    return (_jsxs("div", { className: "template-card", children: [_jsxs("div", { className: "template-card-head", children: [_jsx("span", { className: "template-icon", children: _jsx(Icon, { size: 18, strokeWidth: 2.2 }) }), _jsx("h2", { children: tpl.title })] }), tpl.integrations && tpl.integrations.length > 0 && (_jsx(TemplateIntegrationRow, { slugs: tpl.integrations })), _jsx("p", { className: "template-desc", children: tpl.description }), tpl.tags && tpl.tags.length > 0 && (_jsx("div", { className: "template-tags", children: tpl.tags.map((t) => (_jsx("span", { className: "template-tag", children: t }, t))) })), _jsx("button", { type: "button", className: "primary template-cta", onClick: () => useTemplate(tpl), disabled: busy !== null, children: busy === tpl.id ? t("templates.forking") : t("templates.useTemplate") })] }, tpl.id));
                }) })] }));
}
// templateIntegrationCap is how many brand logos we render before
// collapsing the rest into a "+N" indicator. Four keeps cards visually
// tidy at the most common widths; templates with more integrations
// still surface that they're touching multiple services.
const templateIntegrationCap = 4;
// TemplateIntegrationRow draws a small row of vendor brand icons on
// the template card so users can scan "this template touches Gmail
// and Slack" without reading the title. Each slug maps 1:1 to
// /brands/<slug>.svg under the public assets root; missing files
// produce a broken-image (caught at content-curation time, not a
// render hazard).
function TemplateIntegrationRow({ slugs }) {
    const { t } = useTranslation();
    const shown = slugs.slice(0, templateIntegrationCap);
    const overflow = slugs.length - shown.length;
    return (_jsxs("div", { className: "template-integrations", "aria-label": t("templates.integrationsUsed"), children: [shown.map((slug) => (_jsx("img", { src: `/brands/${slug}.svg`, alt: slug, title: slug, className: "template-integration-logo", draggable: false }, slug))), overflow > 0 && (_jsxs("span", { className: "template-integration-more", title: slugs.slice(templateIntegrationCap).join(", "), children: ["+", overflow] }))] }));
}
