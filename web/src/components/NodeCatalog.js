import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronDown, ChevronRight, Search, Box, Info } from "lucide-react";
import { useTranslation } from "react-i18next";
import { iconFor, isBrandedIcon } from "../icons";
import { integrationSlug } from "../integrationMeta";
// Display label for the standard-library fallback group (drops without
// an Integration). Goes at the bottom and starts collapsed.
const STDLIB_KEY = "__stdlib__";
// Map category keys → i18n keys for the standard-library section's
// sub-headings. Resolved at render time so locale switches refresh
// the labels.
const categoryLabelKey = {
    trigger: "nodeCatalog.categoryTrigger",
    flow_control: "nodeCatalog.categoryFlowControl",
    network: "nodeCatalog.categoryNetwork",
    io: "nodeCatalog.categoryIo",
    ai: "nodeCatalog.categoryAi",
    transformation: "nodeCatalog.categoryTransformation",
    external: "nodeCatalog.categoryExternal",
    system: "nodeCatalog.categorySystem",
};
// integrationIcon maps an Integration display name to one of the icons
// in iconRegistry. Falls back to a generic Box when unknown.
function integrationIcon(name) {
    const k = name.toLowerCase();
    if (k === "git")
        return "git";
    if (k === "ntfy")
        return "ntfy";
    if (k === "claude" || k === "anthropic")
        return "claude";
    if (k === "email")
        return "mail";
    if (k === "http")
        return "globe";
    return undefined;
}
const COLLAPSE_STORAGE_KEY = "hazyflow.catalog.collapsed";
function loadCollapsed() {
    try {
        const raw = localStorage.getItem(COLLAPSE_STORAGE_KEY);
        return raw ? JSON.parse(raw) : { [STDLIB_KEY]: true };
    }
    catch {
        return { [STDLIB_KEY]: true };
    }
}
// stripPrefix removes a leading "<Integration> " from a drop's label so
// rows under "Git" read as "Checkout" / "Build" instead of "Git
// checkout". Case-insensitive prefix match.
function stripPrefix(label, integration) {
    const p = integration + " ";
    if (label.toLowerCase().startsWith(p.toLowerCase())) {
        const rest = label.slice(p.length);
        return rest.charAt(0).toUpperCase() + rest.slice(1);
    }
    return label;
}
export function NodeCatalog({ drops }) {
    const { t } = useTranslation();
    const STDLIB_LABEL = t("nodeCatalog.stdLibLabel");
    const [query, setQuery] = useState("");
    const [collapsed, setCollapsed] = useState(loadCollapsed);
    useEffect(() => {
        localStorage.setItem(COLLAPSE_STORAGE_KEY, JSON.stringify(collapsed));
    }, [collapsed]);
    const toggle = (key) => setCollapsed((prev) => ({ ...prev, [key]: !prev[key] }));
    const filtered = useMemo(() => {
        const q = query.toLowerCase().trim();
        if (!q)
            return drops;
        return drops.filter((m) => m.id.toLowerCase().includes(q) ||
            m.label.toLowerCase().includes(q) ||
            (m.integration ?? "").toLowerCase().includes(q) ||
            (m.description ?? "").toLowerCase().includes(q) ||
            (m.tags ?? []).some((t) => t.toLowerCase().includes(q)));
    }, [drops, query]);
    // Group by Integration when set; everything else lands in a single
    // "Standard library" bucket pinned to the bottom.
    const sections = useMemo(() => {
        const integrations = new Map();
        const stdlib = [];
        for (const m of filtered) {
            if (m.integration) {
                const arr = integrations.get(m.integration) ?? [];
                arr.push(m);
                integrations.set(m.integration, arr);
            }
            else {
                stdlib.push(m);
            }
        }
        const ordered = Array.from(integrations.entries())
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([name, drops]) => {
            // Borrow the brand logo from the first drop in the group
            // that has one. Within an Integration the drops are expected
            // to share a vendor mark; this avoids hardcoding the
            // integration→asset mapping a second time on the UI side.
            let brandLogo;
            for (const d of drops) {
                if (d.brand_logo) {
                    brandLogo = d.brand_logo;
                    break;
                }
            }
            return {
                key: name,
                label: name,
                icon: integrationIcon(name),
                brandLogo,
                drops,
                isStdlib: false,
            };
        });
        if (stdlib.length > 0) {
            ordered.push({
                key: STDLIB_KEY,
                label: STDLIB_LABEL,
                drops: stdlib,
                isStdlib: true,
            });
        }
        return ordered;
    }, [filtered]);
    const onDragStart = (e, m) => {
        e.dataTransfer.setData("application/x-hazyflow-module", m.id);
        e.dataTransfer.effectAllowed = "copy";
    };
    return (_jsxs(_Fragment, { children: [_jsxs("div", { className: "panel-head", children: [_jsx("span", { children: t("nodeCatalog.title") }), _jsx("span", { style: { color: "var(--faint)", fontSize: 11 }, children: drops.length })] }), _jsx("div", { className: "catalog-search", children: _jsxs("div", { style: { position: "relative" }, children: [_jsx(Search, { size: 14, style: {
                                position: "absolute",
                                left: 10,
                                top: "50%",
                                transform: "translateY(-50%)",
                                color: "var(--muted)",
                            } }), _jsx("input", { placeholder: t("nodeCatalog.search"), value: query, onChange: (e) => setQuery(e.target.value), style: { paddingLeft: 30 } })] }) }), _jsx("div", { className: "catalog-list", children: sections.map((s) => {
                    // An active search auto-expands every section so matches
                    // don't hide behind a collapsed header.
                    const isCollapsed = !query && !!collapsed[s.key];
                    const HeaderIcon = s.icon ? iconFor(s.icon) : Box;
                    const headerBranded = isBrandedIcon(s.icon);
                    return (_jsxs("div", { className: "catalog-group", children: [_jsxs("div", { className: "catalog-group-header-row", children: [_jsxs("button", { type: "button", className: "catalog-group-header", onClick: () => toggle(s.key), "aria-expanded": !isCollapsed, children: [isCollapsed ? (_jsx(ChevronRight, { size: 12 })) : (_jsx(ChevronDown, { size: 12 })), !s.isStdlib && (_jsx("span", { className: "catalog-integration-icon", children: s.brandLogo ? (_jsx("img", { src: s.brandLogo, alt: "", className: "catalog-integration-logo", draggable: false })) : (_jsx(HeaderIcon, { size: headerBranded ? 18 : 14, color: headerBranded ? undefined : "currentColor", strokeWidth: 2 })) })), _jsx("span", { className: "catalog-group-label", children: s.label }), _jsx("span", { className: "catalog-group-count", children: s.drops.length })] }), _jsx(Link, { to: `/integrations/${encodeURIComponent(s.isStdlib ? "standard-library" : integrationSlug(s.label))}`, className: "catalog-learn-more", title: t("nodeCatalog.aboutTitle", { name: s.label }), "aria-label": t("nodeCatalog.aboutTitle", { name: s.label }), children: _jsx(Info, { size: 13 }) })] }), !isCollapsed && (_jsx("div", { className: "catalog-group-body", children: s.isStdlib
                                    ? renderStdlib(s.drops, onDragStart, t)
                                    : renderDrops(s.label, s.drops, onDragStart) }))] }, s.key));
                }) })] }));
}
// renderDrops renders the drops inside an Integration section. The label
// strips the integration prefix for shorter reading.
function renderDrops(integration, drops, onDragStart) {
    return (_jsx("div", { style: { display: "flex", flexDirection: "column", gap: 6 }, children: drops.map((m) => (_jsx(DropRow, { drop: m, shortLabel: stripPrefix(m.label, integration), onDragStart: onDragStart }, m.id))) }));
}
// renderStdlib renders the standard-library section, sub-grouped by
// category so flow-control / files / triggers each get their own
// labelled run inside. Takes a translator so subheadings reflect the
// active locale.
function renderStdlib(drops, onDragStart, t) {
    const labelFor = (cat) => categoryLabelKey[cat] ? t(categoryLabelKey[cat]) : cat;
    const byCat = new Map();
    for (const m of drops) {
        const k = m.category ?? "other";
        const arr = byCat.get(k) ?? [];
        arr.push(m);
        byCat.set(k, arr);
    }
    const cats = Array.from(byCat.entries()).sort(([a], [b]) => labelFor(a).localeCompare(labelFor(b)));
    return (_jsx("div", { style: { display: "flex", flexDirection: "column", gap: 12 }, children: cats.map(([cat, items]) => (_jsxs("div", { children: [_jsx("div", { className: "catalog-subhead", children: labelFor(cat) }), _jsx("div", { style: { display: "flex", flexDirection: "column", gap: 6 }, children: items.map((m) => (_jsx(DropRow, { drop: m, shortLabel: m.label, onDragStart: onDragStart }, m.id))) })] }, cat))) }));
}
function DropRow({ drop, shortLabel, onDragStart, }) {
    const { t } = useTranslation();
    const Icon = iconFor(drop.icon, drop.category);
    const color = drop.color ?? "#9f83fe";
    const branded = isBrandedIcon(drop.icon);
    return (_jsxs("div", { className: "module-row", draggable: true, onDragStart: (e) => onDragStart(e, drop), title: drop.description ?? drop.label, children: [drop.brand_logo ? (_jsx("div", { className: "icon brand-logo", children: _jsx("img", { src: drop.brand_logo, alt: "", draggable: false }) })) : branded ? (_jsx("div", { className: "icon branded", children: _jsx(Icon, { size: 24, strokeWidth: 2.2 }) })) : (_jsx("div", { className: "icon", style: {
                    background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
                }, children: _jsx(Icon, { size: 16, color: "#140d30", strokeWidth: 2.2 }) })), _jsxs("div", { style: { minWidth: 0, flex: 1 }, children: [_jsx("div", { className: "name", children: shortLabel }), _jsx("div", { className: "meta", children: drop.id })] }), drop.category && (_jsx("span", { className: "cat-pill", title: t("nodeCatalog.rowCategoryTooltip", { category: drop.category }), children: drop.category }))] }));
}
