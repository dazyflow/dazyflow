import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { Link } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
// Welcome is the post-signup landing wizard — the "first-run"
// surface from the T0-3 TODO. Intentionally simple: three CTAs that
// point at the highest-leverage next actions, plus a confirmation
// of the tenant the user just got. The full step-by-step walkthrough
// (templates gallery, guided node-drop tutorial) becomes useful once
// templates ship; for now this is the right surface for "you're in,
// here's what you can do."
export function Welcome() {
    const { t } = useTranslation();
    const { me } = useAuth();
    return (_jsx("div", { className: "welcome", children: _jsxs("div", { className: "card welcome-card", children: [_jsx("h1", { children: t("welcome.title") }), me?.subject && (_jsxs("p", { className: "welcome-sub", children: [_jsx(Trans, { i18nKey: "welcome.signedInAs", values: { subject: me.subject }, components: [_jsx("strong", {})] }), me.tenant && (_jsx(Trans, { i18nKey: "welcome.inTenant", values: { tenant: me.tenant }, components: [_jsx("code", {})] })), "."] })), _jsx("p", { children: t("welcome.intro") }), _jsxs("ol", { className: "welcome-steps", children: [_jsxs("li", { children: [_jsx("h2", { children: t("welcome.step1Title") }), _jsx("p", { children: t("welcome.step1Body") }), _jsx(Link, { to: "/templates", className: "primary welcome-cta", children: t("welcome.step1Cta") })] }), _jsxs("li", { children: [_jsx("h2", { children: t("welcome.step2Title") }), _jsx("p", { children: t("welcome.step2Body") }), _jsx(Link, { to: "/flows", className: "welcome-cta", children: t("welcome.step2Cta") })] }), _jsxs("li", { children: [_jsx("h2", { children: t("welcome.step3Title") }), _jsx("p", { children: t("welcome.step3Body") }), _jsx(Link, { to: "/runs", className: "welcome-cta", children: t("welcome.step3Cta") })] })] }), _jsx("p", { className: "welcome-foot", children: _jsx(Trans, { i18nKey: "welcome.foot", components: [_jsx(Link, { to: "/welcome" })] }) })] }) }));
}
