// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { NodeResizer, type NodeProps } from "@xyflow/react";
import { Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import i18n from "../../i18n";
import { Button } from "../ui/Button";
import { ICON } from "../../icons";

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
  // Injected by FlowEditor: removes this frame. Frames live in their own
  // state and aren't reachable from the Inspector, so this button is the
  // only delete affordance on touch devices (no Delete/Backspace key).
  onRequestDelete?: () => void;
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
      className="dz-frame"
      style={{
        background: `color-mix(in srgb, ${color} 9%, transparent)`,
        borderColor: `color-mix(in srgb, ${color} 55%, var(--border))`,
      }}
    >
      {/* Drag handles to resize the box; only shown while selected. */}
      <NodeResizer color={color} isVisible={!!selected} minWidth={140} minHeight={90} />
      {selected && d.onRequestDelete && (
        <Button
          className="dz-frame-delete nodrag nopan"
          aria-label={i18n.t("commentNode.delete")}
          title={i18n.t("commentNode.delete")}
          onClick={(e) => {
            // Stop the canvas from re-selecting/dragging on the same tap.
            e.stopPropagation();
            d.onRequestDelete?.();
          }}
        >
          <Trash2 size={ICON.sm} />
        </Button>
      )}
      <input
        className="dz-frame-title nodrag"
        value={title}
        placeholder={i18n.t("commentNode.placeholder")}
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
