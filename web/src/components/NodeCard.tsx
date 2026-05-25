import { Handle, Position, type NodeProps } from "@xyflow/react";
import { iconFor } from "../icons";
import type { Manifest } from "../types";

// HazyNodeData is the shape we stash on each React Flow node. We carry
// the live manifest so the canvas can render the same icon and label as
// the catalog without a second lookup.
export type HazyNodeData = {
  label: string;
  moduleID: string;
  manifest?: Manifest;
  status?: string; // run status from the SSE stream
};

export function HazyNode({ data, selected }: NodeProps) {
  const d = data as HazyNodeData;
  const Icon = iconFor(d.manifest?.icon, d.manifest?.category);
  const color = d.manifest?.color ?? "#9f83fe";
  return (
    <div className={"hz-node" + (selected ? " selected" : "")}>
      <Handle
        type="target"
        position={Position.Left}
        style={{
          background: "var(--surface-3)",
          border: "1px solid var(--border-strong)",
          width: 10,
          height: 10,
        }}
      />
      <div
        className="icon"
        style={{
          background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
        }}
      >
        <Icon size={16} color="#140d30" strokeWidth={2.2} />
      </div>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div className="label">{d.label}</div>
        <div className="module-id">{d.moduleID}</div>
      </div>
      {d.status && (
        <div
          className={"status-dot " + d.status}
          title={`status: ${d.status}`}
        />
      )}
      <Handle
        type="source"
        position={Position.Right}
        style={{
          background: "var(--surface-3)",
          border: "1px solid var(--border-strong)",
          width: 10,
          height: 10,
        }}
      />
    </div>
  );
}
