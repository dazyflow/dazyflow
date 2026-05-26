import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { Handle, Position } from "@xyflow/react";
import i18n from "../i18n";
import { iconFor, isBrandedIcon } from "../icons";
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
    const statusClass = d.status ? " status-" + d.status : "";
    return (_jsxs("div", { className: "hz-node" + (selected ? " selected" : "") + statusClass, children: [inputs.map((p, i) => (_jsx(Handle, { type: "target", position: Position.Left, id: p.port, style: portStyle(p, i, inputs.length, "left"), title: portTooltip(p) }, "in-" + p.port))), d.manifest?.brand_logo ? (_jsx("div", { className: "icon brand-logo", children: _jsx("img", { src: d.manifest.brand_logo, alt: "", draggable: false }) })) : isBrandedIcon(d.manifest?.icon) ? (_jsx("div", { className: "icon branded", children: _jsx(Icon, { size: 22, strokeWidth: 2.2 }) })) : (_jsx("div", { className: "icon", style: {
                    background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
                }, children: _jsx(Icon, { size: 16, color: "#140d30", strokeWidth: 2.2 }) })), _jsxs("div", { className: "hz-node-body", children: [_jsx("div", { className: "label", children: d.label }), _jsx("div", { className: "module-id", children: d.moduleID }), (inputsMulti || outputsMulti) && (_jsxs("div", { className: "hz-ports", children: [inputsMulti && (_jsx("div", { className: "hz-port-col", children: inputs.map((p) => (_jsx("div", { className: "hz-port-label hz-port-in", children: p.port }, "il-" + p.port))) })), outputsMulti && (_jsx("div", { className: "hz-port-col right", children: outputs.map((p) => (_jsx("div", { className: "hz-port-label hz-port-out", children: p.port }, "ol-" + p.port))) }))] }))] }), d.status && (_jsx("div", { className: "status-dot " + d.status, title: i18n.t("nodeCard.statusTooltip", { status: d.status }) })), outputs.map((p, i) => (_jsx(Handle, { type: "source", position: Position.Right, id: p.port, style: portStyle(p, i, outputs.length, "right"), title: portTooltip(p) }, "out-" + p.port)))] }));
}
// portStyle places each handle's vertical center and paints it
// according to the port's MIME (color) and required-ness (filled vs
// hollow). Single ports sit at the card's vertical midpoint; multiple
// ports spread evenly inside the labels region (which we pad to match
// in CSS, see .hz-ports).
//
// Geometry: the handle vertical position is computed in pixels off the
// card top because React Flow's default Position.{Left,Right} centers
// at top:50% — we override with style.top.
//
// Visual encoding:
//   - color    → first listed MIME on the port (see portColor)
//   - fill     → required ports are solid; optional ports are hollow
//                rings of the same color, signalling "you don't have to
//                wire this to make the graph valid"
//   - missing MIME falls back to the neutral surface color so ports on
//     legacy manifests without MIME annotations look unchanged
function portStyle(port, index, count, side) {
    const color = portColor(port.mime);
    const required = port.required ?? false;
    const base = {
        background: required ? color : "var(--surface)",
        border: `1.5px solid ${color}`,
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
// portColor picks a hue from the port's first listed MIME. Three rules
// of thumb apply: keep the palette small (≤5 hues) so the canvas stays
// readable, prefer broad MIME prefixes over exact strings so unknown
// subtypes still get a sensible color, and fall back to the neutral
// border color for ports that don't declare a MIME (the common case for
// legacy manifests we haven't yet annotated).
function portColor(mime) {
    if (!mime || mime.length === 0)
        return "var(--border-strong)";
    const m = mime[0];
    if (m.startsWith("text/"))
        return "#4a8"; // green — plain text
    if (m === "application/json")
        return "#5b8def"; // blue  — structured data
    if (m.startsWith("image/"))
        return "#e8a85e"; // amber — images
    if (m.startsWith("audio/") || m.startsWith("video/"))
        return "#c87fff"; // purple — media
    if (m.startsWith("application/"))
        return "#9a9a9a"; // gray  — generic binary/file
    return "var(--border-strong)";
}
// portTooltip is rendered as the handle's HTML title attribute — the
// browser shows it on hover. Cheap discoverability for single-port
// nodes where there's no in-card port label to read.
function portTooltip(port) {
    const parts = [port.label ? `${port.label} (${port.port})` : port.port];
    if (port.mime && port.mime.length > 0)
        parts.push(port.mime.join(" | "));
    parts.push(port.required ? i18n.t("nodeCard.portRequired") : i18n.t("nodeCard.portOptional"));
    return parts.join(" — ");
}
