import { Handle, Position, type NodeProps } from "@xyflow/react";
import { iconFor } from "../icons";
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

  return (
    <div className={"hz-node" + (selected ? " selected" : "")}>
      {/* Inputs (left side). Single-port nodes get a centered dot;
          multi-port nodes get one handle per port spread vertically. */}
      {inputs.map((p, i) => (
        <Handle
          key={"in-" + p.port}
          type="target"
          position={Position.Left}
          id={p.port}
          style={portStyle(i, inputs.length, "left")}
        />
      ))}

      <div
        className="icon"
        style={{
          background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
        }}
      >
        <Icon size={16} color="#140d30" strokeWidth={2.2} />
      </div>
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
          style={portStyle(i, outputs.length, "right")}
        />
      ))}
    </div>
  );
}

// portStyle places each handle's vertical center. Single ports sit at
// the card's vertical midpoint; multiple ports spread evenly inside the
// labels region (which we pad to match in CSS, see .hz-ports).
//
// Geometry: the handle vertical position is computed in pixels off the
// card top because React Flow's default Position.{Left,Right} centers
// at top:50% — we override with style.top.
function portStyle(index: number, count: number, side: "left" | "right") {
  const base = {
    background: "var(--surface-3)",
    border: "1px solid var(--border-strong)",
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
