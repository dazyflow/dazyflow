import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Box } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { iconFor, isBrandedIcon } from "../icons";
import { integrationMeta, integrationNameFromSlug, integrationSlug, } from "../integrationMeta";
// Integrations is the index page — one card per integration the
// daemon knows about, derived from the live manifest registry plus
// curated prose from integrationMeta. Drops without an Integration
// field land in a "Standard library" bucket so the page covers
// everything the catalog shows in the editor.
//
// Card-level data:
//   - brand logo (from any drop's brand_logo, or curated override)
//   - integration name (curated, falls back to slug)
//   - short description (curated; truncated; full prose on detail)
//   - drop count
export function Integrations() {
    const { t } = useTranslation();
    const { token } = useAuth();
    const [drops, setDrops] = useState(null);
    const [error, setError] = useState(null);
    useEffect(() => {
        if (!token)
            return;
        api
            .listDrops(token)
            .then((r) => setDrops(r.drops))
            .catch((e) => {
            const msg = e instanceof APIError ? `${e.status}: ${e.message}` : e.message;
            setError(msg);
        });
    }, [token]);
    // Group drops by integration slug. The standard-library bucket
    // catches anything without an Integration field — matches the
    // NodeCatalog grouping rules.
    const groups = useMemo(() => buildGroups(drops ?? []), [drops]);
    if (error) {
        return (_jsxs("div", { className: "page", children: [_jsx("h1", { children: t("integrations.title") }), _jsx("div", { className: "card error", children: error })] }));
    }
    if (!drops) {
        return (_jsxs("div", { className: "page", children: [_jsx("h1", { children: t("integrations.title") }), _jsx("div", { className: "card", children: t("common.loading") })] }));
    }
    return (_jsxs("div", { className: "page integrations-page", children: [_jsx("h1", { children: t("integrations.title") }), _jsx("p", { className: "page-sub", children: t("integrations.intro") }), _jsx("div", { className: "integration-grid", children: groups.map(({ slug, meta, drops }) => {
                    // Logo fallback chain: curated override → any drop's
                    // brand_logo → category-derived lucide glyph from the
                    // first drop. Drops carrying their own brand_logo means
                    // we render the right vendor mark even for integrations
                    // without a curated metadata entry (excel, mysql,
                    // postgres, sqlite all ship per-drop logos).
                    const brandLogo = meta.brand_logo ?? drops.find((d) => d.brand_logo)?.brand_logo;
                    const headerDrop = drops[0];
                    const HeaderIcon = headerDrop
                        ? iconFor(headerDrop.icon, headerDrop.category)
                        : Box;
                    const headerBranded = isBrandedIcon(headerDrop?.icon);
                    return (_jsx(Link, { to: `/integrations/${encodeURIComponent(slug)}`, style: { textDecoration: "none", color: "inherit" }, children: _jsxs("div", { className: "integration-card", children: [_jsxs("div", { className: "integration-card-head", children: [brandLogo ? (_jsx("img", { src: brandLogo, alt: "", className: "integration-card-logo", draggable: false })) : (_jsx("span", { className: "integration-card-fallback-icon", children: _jsx(HeaderIcon, { size: headerBranded ? 22 : 18, strokeWidth: 2.2 }) })), _jsx("h2", { children: meta.name })] }), _jsx("p", { className: "integration-card-desc", children: truncate(meta.description, 160) }), _jsx("div", { className: "integration-card-meta", children: _jsx("span", { className: "integration-card-count", children: t("integrations.drop", { count: drops.length }) }) })] }) }, slug));
                }) })] }));
}
// IntegrationDetail is /integrations/:slug — the per-integration
// "profile" page. Shows the hero (logo + name + full prose) plus
// every drop the integration ships, each with its description,
// input/output ports, and a collapsed params hint.
export function IntegrationDetail() {
    const { t } = useTranslation();
    const slugRaw = window.location.pathname.split("/").pop() ?? "";
    const slug = decodeURIComponent(slugRaw);
    const { token } = useAuth();
    const [drops, setDrops] = useState(null);
    const [error, setError] = useState(null);
    useEffect(() => {
        if (!token)
            return;
        api
            .listDrops(token)
            .then((r) => setDrops(r.drops))
            .catch((e) => {
            const msg = e instanceof APIError ? `${e.status}: ${e.message}` : e.message;
            setError(msg);
        });
    }, [token]);
    const { meta, integrationDrops } = useMemo(() => {
        const all = drops ?? [];
        const filtered = all.filter((m) => integrationSlugFor(m) === slug);
        const m = integrationMeta[slug] ?? {
            name: integrationNameFromSlug(slug),
            description: "",
        };
        return { meta: m, integrationDrops: filtered };
    }, [drops, slug]);
    if (error) {
        return (_jsxs("div", { className: "page", children: [_jsx("h1", { children: meta.name }), _jsx("div", { className: "card error", children: error })] }));
    }
    if (!drops) {
        return (_jsx("div", { className: "page", children: _jsx("div", { className: "card", children: t("common.loading") }) }));
    }
    if (integrationDrops.length === 0) {
        return (_jsxs("div", { className: "page", children: [_jsx("h1", { children: meta.name }), _jsx(Link, { to: "/integrations", className: "back-link", children: t("integrations.backAll") }), _jsx("div", { className: "card", style: { marginTop: 12 }, children: t("integrations.noDrops") })] }));
    }
    // Pick a brand logo: curated override wins, otherwise borrow from
    // the first drop with one.
    const brandLogo = meta.brand_logo ?? integrationDrops.find((d) => d.brand_logo)?.brand_logo;
    return (_jsxs("div", { className: "page integration-detail", children: [_jsx(Link, { to: "/integrations", className: "back-link", children: t("integrations.backAll") }), _jsxs("header", { className: "integration-hero", children: [brandLogo && (_jsx("img", { src: brandLogo, alt: "", className: "integration-hero-logo", draggable: false })), _jsxs("div", { children: [_jsx("h1", { children: meta.name }), meta.description && (_jsx("p", { className: "integration-hero-desc", children: meta.description })), meta.technical_notes && (_jsxs("details", { className: "integration-hero-technical", children: [_jsx("summary", { children: t("integrations.technicalDetails") }), _jsx("p", { children: meta.technical_notes })] })), meta.docs_url && (_jsx("p", { className: "integration-hero-docs", children: _jsx("a", { href: meta.docs_url, target: "_blank", rel: "noreferrer noopener", children: t("integrations.officialDocs") }) }))] })] }), _jsx("h2", { className: "integration-drops-head", children: t("integrations.dropsHead") }), _jsx("div", { className: "integration-drops", children: integrationDrops.map((d) => (_jsx(DropCard, { drop: d }, d.id))) })] }));
}
// DropCard renders one drop's "help" entry: icon + label + module
// ID, full description, input + output ports, and a collapsed view
// of the params schema (rendered as a JSON dump under a <details>).
function DropCard({ drop }) {
    const { t } = useTranslation();
    const Icon = iconFor(drop.icon, drop.category);
    const branded = isBrandedIcon(drop.icon);
    const color = drop.color ?? "#9f83fe";
    return (_jsxs("div", { className: "drop-card", children: [_jsxs("div", { className: "drop-card-head", children: [drop.brand_logo ? (_jsx("div", { className: "icon brand-logo", children: _jsx("img", { src: drop.brand_logo, alt: "", draggable: false }) })) : branded ? (_jsx("div", { className: "icon branded", children: _jsx(Icon, { size: 22, strokeWidth: 2.2 }) })) : (_jsx("div", { className: "icon", style: {
                            background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
                        }, children: _jsx(Icon, { size: 16, color: "#140d30", strokeWidth: 2.2 }) })), _jsxs("div", { className: "drop-card-title", children: [_jsx("h3", { children: drop.label }), _jsx("code", { className: "drop-card-id", children: drop.id })] })] }), drop.description && (_jsx("p", { className: "drop-card-desc", children: drop.description })), hasWiringDetails(drop) && (_jsxs("details", { className: "drop-card-wiring", children: [_jsx("summary", { children: t("integrations.wiringDetails") }), _jsxs("div", { className: "drop-card-ports", children: [drop.inputs && drop.inputs.length > 0 && (_jsxs("div", { children: [_jsx("div", { className: "drop-card-port-head", children: t("integrations.inputs") }), _jsx("ul", { children: drop.inputs.map((p) => (_jsxs("li", { children: [_jsx("code", { children: p.port }), p.required && (_jsxs("span", { className: "port-required", children: [" ", t("integrations.required")] })), p.label && (_jsxs("span", { className: "port-label", children: [" \u2014 ", p.label] }))] }, p.port))) })] })), drop.outputs && drop.outputs.length > 0 && (_jsxs("div", { children: [_jsx("div", { className: "drop-card-port-head", children: t("integrations.outputs") }), _jsx("ul", { children: drop.outputs.map((p) => (_jsxs("li", { children: [_jsx("code", { children: p.port }), p.label && (_jsxs("span", { className: "port-label", children: [" \u2014 ", p.label] }))] }, p.port))) })] }))] }), drop.params_schema && (_jsxs("div", { className: "drop-card-params-block", children: [_jsx("div", { className: "drop-card-port-head", children: t("integrations.paramsSchema") }), _jsx("pre", { children: JSON.stringify(drop.params_schema, null, 2) })] }))] }))] }));
}
// hasWiringDetails reports whether a drop has any of the
// technical-flavored fields the disclosure would reveal. Drops
// with no inputs, no outputs, and no params schema would render an
// empty disclosure — skip the summary entirely in that case so
// the card stays clean.
function hasWiringDetails(d) {
    return ((d.inputs && d.inputs.length > 0) ||
        (d.outputs && d.outputs.length > 0) ||
        !!d.params_schema);
}
// integrationSlugFor returns the slug a drop belongs to. Drops
// without an Integration field land in "standard-library" — same
// rule the NodeCatalog uses for grouping.
function integrationSlugFor(m) {
    if (m.integration && m.integration.trim() !== "") {
        return integrationSlug(m.integration);
    }
    return "standard-library";
}
// buildGroups maps drops into the displayable group list. The
// curated integrationMeta entries are surfaced first (in
// declaration order) so the most-polished integrations appear at
// the top; any uncurated slugs that still have drops get tacked on
// alphabetically at the end.
function buildGroups(all) {
    const bySlug = new Map();
    for (const m of all) {
        const slug = integrationSlugFor(m);
        const arr = bySlug.get(slug) ?? [];
        arr.push(m);
        bySlug.set(slug, arr);
    }
    const out = [];
    const seen = new Set();
    for (const slug of Object.keys(integrationMeta)) {
        if (!bySlug.has(slug))
            continue;
        out.push({ slug, meta: integrationMeta[slug], drops: bySlug.get(slug) });
        seen.add(slug);
    }
    const tail = Array.from(bySlug.keys())
        .filter((s) => !seen.has(s))
        .sort();
    for (const slug of tail) {
        out.push({
            slug,
            meta: {
                name: integrationNameFromSlug(slug),
                description: "",
            },
            drops: bySlug.get(slug),
        });
    }
    return out;
}
function truncate(s, n) {
    if (s.length <= n)
        return s;
    // .replace stays inside the ES2015 lib target the rest of the
    // app builds against (avoids forcing es2019.string just for trimEnd).
    return s.slice(0, n - 1).replace(/\s+$/, "") + "…";
}
