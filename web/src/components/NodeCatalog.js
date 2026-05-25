import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useMemo, useState } from "react";
import { Search } from "lucide-react";
import { iconFor } from "../icons";
// Mapping from category keys to display labels — keeps the UI in one
// place even as categories evolve on the backend.
const categoryLabel = {
    trigger: "Triggers",
    flow_control: "Flow control",
    network: "Network",
    io: "I/O",
    ai: "AI",
    transformation: "Transform",
    external: "External",
    system: "System",
};
export function NodeCatalog({ modules }) {
    const [query, setQuery] = useState("");
    const filtered = useMemo(() => {
        const q = query.toLowerCase().trim();
        if (!q)
            return modules;
        return modules.filter((m) => m.id.toLowerCase().includes(q) ||
            m.label.toLowerCase().includes(q) ||
            (m.description ?? "").toLowerCase().includes(q) ||
            (m.tags ?? []).some((t) => t.toLowerCase().includes(q)));
    }, [modules, query]);
    // Group by category for visual organization.
    const groups = useMemo(() => {
        const map = new Map();
        for (const m of filtered) {
            const k = m.category ?? "other";
            const arr = map.get(k) ?? [];
            arr.push(m);
            map.set(k, arr);
        }
        return Array.from(map.entries()).sort(([a], [b]) => a.localeCompare(b));
    }, [filtered]);
    const onDragStart = (e, m) => {
        e.dataTransfer.setData("application/x-hazyflow-module", m.id);
        e.dataTransfer.effectAllowed = "copy";
    };
    return (_jsxs(_Fragment, { children: [_jsxs("div", { className: "panel-head", children: [_jsx("span", { children: "Nodes" }), _jsx("span", { style: { color: "var(--faint)", fontSize: 11 }, children: modules.length })] }), _jsx("div", { className: "catalog-search", children: _jsxs("div", { style: { position: "relative" }, children: [_jsx(Search, { size: 14, style: {
                                position: "absolute",
                                left: 10,
                                top: "50%",
                                transform: "translateY(-50%)",
                                color: "var(--muted)",
                            } }), _jsx("input", { placeholder: "Search modules\u2026", value: query, onChange: (e) => setQuery(e.target.value), style: { paddingLeft: 30 } })] }) }), _jsx("div", { className: "catalog-list", children: groups.map(([cat, mods]) => (_jsxs("div", { className: "catalog-group", children: [_jsx("h3", { children: categoryLabel[cat] ?? cat }), _jsx("div", { style: { display: "flex", flexDirection: "column", gap: 6 }, children: mods.map((m) => {
                                const Icon = iconFor(m.icon, m.category);
                                const color = m.color ?? "#9f83fe";
                                return (_jsxs("div", { className: "module-row", draggable: true, onDragStart: (e) => onDragStart(e, m), title: m.description ?? m.label, children: [_jsx("div", { className: "icon", style: {
                                                background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
                                            }, children: _jsx(Icon, { size: 16, color: "#140d30", strokeWidth: 2.2 }) }), _jsxs("div", { style: { minWidth: 0 }, children: [_jsx("div", { className: "name", children: m.label }), _jsx("div", { className: "meta", children: m.id })] })] }, m.id));
                            }) })] }, cat))) })] }));
}
