import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { Handle, Position } from "@xyflow/react";
import { iconFor } from "../icons";
export function HazyNode({ data, selected }) {
    const d = data;
    const Icon = iconFor(d.manifest?.icon, d.manifest?.category);
    const color = d.manifest?.color ?? "#9f83fe";
    return (_jsxs("div", { className: "hz-node" + (selected ? " selected" : ""), children: [_jsx(Handle, { type: "target", position: Position.Left, style: {
                    background: "var(--surface-3)",
                    border: "1px solid var(--border-strong)",
                    width: 10,
                    height: 10,
                } }), _jsx("div", { className: "icon", style: {
                    background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
                }, children: _jsx(Icon, { size: 16, color: "#140d30", strokeWidth: 2.2 }) }), _jsxs("div", { style: { flex: 1, minWidth: 0 }, children: [_jsx("div", { className: "label", children: d.label }), _jsx("div", { className: "module-id", children: d.moduleID })] }), d.status && (_jsx("div", { className: "status-dot " + d.status, title: `status: ${d.status}` })), _jsx(Handle, { type: "source", position: Position.Right, style: {
                    background: "var(--surface-3)",
                    border: "1px solid var(--border-strong)",
                    width: 10,
                    height: 10,
                } })] }));
}
