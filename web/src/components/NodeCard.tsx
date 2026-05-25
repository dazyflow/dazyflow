import { Handle, Position, type NodeProps } from "@xyflow/react";
import { iconFor, isBrandedIcon } from "../icons";
import type { Manifest, Port } from "../types";

// HazyNodeData is the shape we stash on each React Flow node. We carry
// the live manifest so the canvas can render the same icon and label as
// the catalog without a second lookup.
export type HazyNodeData = {
  label: string;
  moduleID: string;
  manifest?: Manifest;
  status?: string;
};

// Layout: nodes with a single input port and a single output port
// stay compact — handles dot the left and right edges with no labels.
// Once a side has more than one port we render its labels inside the
// card so the wiring is unambiguous (no "which handle was that?"
// guesswork). The canonical multi-port shapes are branch (then/else)
// and await_approval (approved/rejected).
export function HazyNode({ data, selected }: NodeProps) {
  const d = data as HazyNodeData;
  const Icon = iconFor(d.manifest?.icon, d.manifest?.category);
  const color = d.manifest?.color ?? "#9f83fe";

  // Default to "in"/"out" when the manifest didn't ship port lists —
  // matches the engine's fallback ports.
  const inputs: Port[] = d.manifest?.inputs?.length
    ? d.manifest.inputs
    : [{ port: "in" }];
  const outputs: Port[] = d.manifest?.outputs?.length
    ? d.manifest.outputs
    : [{ port: "out" }];
  const inputsMulti = inputs.length > 1;
  const outputsMulti = outputs.length > 1;

  const statusClass = d.status ? " status-" + d.status : "";

  return (
    <div className={"hz-node" + (selected ? " selected" : "") + statusClass}>
      {/* Inputs (left side). Single-port nodes get a centered dot;
          multi-port nodes get one handle per port spread vertically. */}
      {inputs.map((p, i) => (
        <Handle
          key={"in-" + p.port}
          type="target"
          position={Position.Left}
          id={p.port}
          style={portStyle(p, i, inputs.length, "left")}
          title={portTooltip(p)}
        />
      ))}

      {isBrandedIcon(d.manifest?.icon) ? (
        <div className="icon branded">
          <Icon size={22} strokeWidth={2.2} />
        </div>
      ) : (
        <div
          className="icon"
          style={{
            background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
          }}
        >
          <Icon size={16} color="#140d30" strokeWidth={2.2} />
        </div>
      )}
      <div className="hz-node-body">
        <div className="label">{d.label}</div>
        <div className="module-id">{d.moduleID}</div>
        {(inputsMulti || outputsMulti) && (
          <div className="hz-ports">
            {inputsMulti && (
              <div className="hz-port-col">
                {inputs.map((p) => (
                  <div key={"il-" + p.port} className="hz-port-label hz-port-in">
                    {p.port}
                  </div>
                ))}
              </div>
            )}
            {outputsMulti && (
              <div className="hz-port-col right">
                {outputs.map((p) => (
                  <div key={"ol-" + p.port} className="hz-port-label hz-port-out">
                    {p.port}
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
      {d.status && (
        <div
          className={"status-dot " + d.status}
          title={`status: ${d.status}`}
        />
      )}

      {/* Outputs (right side). */}
      {outputs.map((p, i) => (
        <Handle
          key={"out-" + p.port}
          type="source"
          position={Position.Right}
          id={p.port}
          style={portStyle(p, i, outputs.length, "right")}
          title={portTooltip(p)}
        />
      ))}
    </div>
  );
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
function portStyle(port: Port, index: number, count: number, side: "left" | "right") {
  const color = portColor(port.mime);
  const required = port.required ?? false;
  const base = {
    background: required ? color : "var(--surface)",
    border: `1.5px solid ${color}`,
    width: 10,
    height: 10,
  } as const;
  if (count === 1) return base;
  // 38px is the header band height; below that we have the
  // labels block whose rows are 18px tall. Center each handle on the
  // matching label row.
  const top = 50 + index * 20;
  return {
    ...base,
    top: `${top}px`,
    ...(side === "left" ? { left: -5 } : { right: -5 }),
  } as const;
}

// portColor picks a hue from the port's first listed MIME. Three rules
// of thumb apply: keep the palette small (≤5 hues) so the canvas stays
// readable, prefer broad MIME prefixes over exact strings so unknown
// subtypes still get a sensible color, and fall back to the neutral
// border color for ports that don't declare a MIME (the common case for
// legacy manifests we haven't yet annotated).
function portColor(mime: string[] | undefined): string {
  if (!mime || mime.length === 0) return "var(--border-strong)";
  const m = mime[0];
  if (m.startsWith("text/")) return "#4a8";              // green — plain text
  if (m === "application/json") return "#5b8def";        // blue  — structured data
  if (m.startsWith("image/")) return "#e8a85e";          // amber — images
  if (m.startsWith("audio/") || m.startsWith("video/")) return "#c87fff"; // purple — media
  if (m.startsWith("application/")) return "#9a9a9a";    // gray  — generic binary/file
  return "var(--border-strong)";
}

// portTooltip is rendered as the handle's HTML title attribute — the
// browser shows it on hover. Cheap discoverability for single-port
// nodes where there's no in-card port label to read.
function portTooltip(port: Port): string {
  const parts = [port.label ? `${port.label} (${port.port})` : port.port];
  if (port.mime && port.mime.length > 0) parts.push(port.mime.join(" | "));
  parts.push(port.required ? "required" : "optional");
  return parts.join(" — ");
}
