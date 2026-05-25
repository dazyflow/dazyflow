import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { Handle, Position } from "@xyflow/react";
import { iconFor } from "../icons";
// Layout: nodes with a single input port and a single output port
// stay compact — handles dot the left and right edges with no labels.
// Once a side has more than one port we render its labels inside the
// card so the wiring is unambiguous (no "which handle was that?"
// guesswork). The canonical multi-port shapes are branch (then/else)
// and await_approval (approved/rejected).
export function HazyNode({ data, selected }) {
    const d = data;
    const Icon = iconFor(d.manifest?.icon, d.manifest?.category);
    const color = d.manifest?.color ?? "#9f83fe";
    // Default to "in"/"out" when the manifest didn't ship port lists —
    // matches the engine's fallback ports.
    const inputs = d.manifest?.inputs?.length
        ? d.manifest.inputs
        : [{ port: "in" }];
    const outputs = d.manifest?.outputs?.length
        ? d.manifest.outputs
        : [{ port: "out" }];
    const inputsMulti = inputs.length > 1;
    const outputsMulti = outputs.length > 1;
    return (_jsxs("div", { className: "hz-node" + (selected ? " selected" : ""), children: [inputs.map((p, i) => (_jsx(Handle, { type: "target", position: Position.Left, id: p.port, style: portStyle(i, inputs.length, "left") }, "in-" + p.port))), _jsx("div", { className: "icon", style: {
                    background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
                }, children: _jsx(Icon, { size: 16, color: "#140d30", strokeWidth: 2.2 }) }), _jsxs("div", { className: "hz-node-body", children: [_jsx("div", { className: "label", children: d.label }), _jsx("div", { className: "module-id", children: d.moduleID }), (inputsMulti || outputsMulti) && (_jsxs("div", { className: "hz-ports", children: [inputsMulti && (_jsx("div", { className: "hz-port-col", children: inputs.map((p) => (_jsx("div", { className: "hz-port-label hz-port-in", children: p.port }, "il-" + p.port))) })), outputsMulti && (_jsx("div", { className: "hz-port-col right", children: outputs.map((p) => (_jsx("div", { className: "hz-port-label hz-port-out", children: p.port }, "ol-" + p.port))) }))] }))] }), d.status && (_jsx("div", { className: "status-dot " + d.status, title: `status: ${d.status}` })), outputs.map((p, i) => (_jsx(Handle, { type: "source", position: Position.Right, id: p.port, style: portStyle(i, outputs.length, "right") }, "out-" + p.port)))] }));
}
// portStyle places each handle's vertical center. Single ports sit at
// the card's vertical midpoint; multiple ports spread evenly inside the
// labels region (which we pad to match in CSS, see .hz-ports).
//
// Geometry: the handle vertical position is computed in pixels off the
// card top because React Flow's default Position.{Left,Right} centers
// at top:50% — we override with style.top.
function portStyle(index, count, side) {
    const base = {
        background: "var(--surface-3)",
        border: "1px solid var(--border-strong)",
        width: 10,
        height: 10,
    };
    if (count === 1)
        return base;
    // 38px is the header band height; below that we have the
    // labels block whose rows are 18px tall. Center each handle on the
    // matching label row.
    const top = 50 + index * 20;
    return {
        ...base,
        top: `${top}px`,
        ...(side === "left" ? { left: -5 } : { right: -5 }),
    };
}
