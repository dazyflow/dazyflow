import { NodeResizer, type NodeProps } from "@xyflow/react";
import { useEffect, useState } from "react";

// CommentNode is an editor-only "frame": a resizable, titled, colored box
// that sits behind the real nodes to group them visually (#3). It has no
// ports and never participates in execution — FlowEditor keeps frames in
// their own state and serializes them to the graph's `frames` metadata,
// which the engine ignores.
export type CommentData = {
  title?: string;
  color?: string;
  // Injected by FlowEditor so title edits land in the controlled frame
  // state (and mark the graph dirty).
  onTitleChange?: (title: string) => void;
};

export function CommentNode({ data, selected }: NodeProps) {
  const d = data as CommentData;
  const color = d.color ?? "#9f83fe";
  // Local mirror so typing stays responsive; synced from props on load.
  const [title, setTitle] = useState(d.title ?? "");
  useEffect(() => {
    setTitle(d.title ?? "");
  }, [d.title]);

  return (
    <div
      className="hz-frame"
      style={{
        background: `color-mix(in srgb, ${color} 9%, transparent)`,
        borderColor: `color-mix(in srgb, ${color} 55%, var(--border))`,
      }}
    >
      {/* Drag handles to resize the box; only shown while selected. */}
      <NodeResizer color={color} isVisible={!!selected} minWidth={140} minHeight={90} />
      <input
        className="hz-frame-title nodrag"
        value={title}
        placeholder="Comment"
        spellCheck={false}
        onChange={(e) => {
          setTitle(e.target.value);
          d.onTitleChange?.(e.target.value);
        }}
        style={{ color }}
      />
    </div>
  );
}
