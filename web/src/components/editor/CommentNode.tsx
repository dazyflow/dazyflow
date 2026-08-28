// SPDX-FileCopyrightText: 2026 Angels' Ware
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
  // Injected by FlowEditor: the swatch row writes the picked color back into
  // frame state the same way. Absent (like onTitleChange) on a read-only
  // canvas, which is what hides the row.
  onColorChange?: (color: string) => void;
  // Injected by FlowEditor: removes this frame. Frames live in their own
  // state and aren't reachable from the Inspector, so this button is the
  // only delete affordance on touch devices (no Delete/Backspace key).
  onRequestDelete?: () => void;
};

// FRAME_COLORS is what a note can be tinted with: a fixed palette, not a free
// color input. The tint is mixed at 9% over the canvas and at 55% into the
// border, in both themes — a picker offers thousands of values and most of them
// come out as an invisible wash or an unreadable border on one of the two. The
// hues are the ones the canvas already uses for drop categories (see
// categoryColors in icons.tsx), so a colored note reads as part of the same
// drawing rather than a sticker on top of it.
//
// `name` keys into commentNode.colors.* — a swatch needs an accessible name,
// and "the third dot" is not one.
export const FRAME_COLORS = [
  { hex: "#9f83fe", name: "violet" }, // the default; the app accent
  { hex: "#5a9bd4", name: "blue" },
  { hex: "#46c46e", name: "green" },
  { hex: "#e0a45e", name: "amber" },
  { hex: "#ff6464", name: "red" },
  { hex: "#9e9abb", name: "slate" }, // the neutral, for a note that shouldn't shout
] as const;

export const FRAME_COLOR_DEFAULT = FRAME_COLORS[0].hex;

export function CommentNode({ data, selected }: NodeProps) {
  const d = data as CommentData;
  const color = d.color ?? FRAME_COLOR_DEFAULT;
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
      {/* Title and tools share one wrapping flex row. The tools were absolutely
          positioned while delete was the only one; a second control there would
          have had to be placed by hand against a box the user resizes, and on a
          narrow note the two would have sat on top of the title. */}
      <div className="dz-frame-head">
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
        {selected && (d.onColorChange || d.onRequestDelete) && (
          <div className="dz-frame-tools nodrag nopan">
            {d.onColorChange && (
              <div
                className="dz-frame-colors"
                role="group"
                aria-label={i18n.t("commentNode.color")}
              >
                {FRAME_COLORS.map((c) => {
                  const on = c.hex.toLowerCase() === color.toLowerCase();
                  const label = i18n.t(`commentNode.colors.${c.name}`);
                  return (
                    // A swatch is selectable state, not an action, so it stays
                    // outside the Button vocabulary — the `.active` family, the
                    // same idiom as the theme picker.
                    <button
                      key={c.hex}
                      type="button"
                      className={"dz-frame-swatch" + (on ? " active" : "")}
                      style={{ background: c.hex }}
                      aria-pressed={on}
                      aria-label={label}
                      title={label}
                      onClick={(e) => {
                        // Stop the canvas from re-selecting/dragging on the
                        // same tap.
                        e.stopPropagation();
                        d.onColorChange?.(c.hex);
                      }}
                    />
                  );
                })}
              </div>
            )}
            {d.onRequestDelete && (
              <Button
                className="dz-frame-delete"
                aria-label={i18n.t("commentNode.delete")}
                title={i18n.t("commentNode.delete")}
                onClick={(e) => {
                  e.stopPropagation();
                  d.onRequestDelete?.();
                }}
              >
                <Trash2 size={ICON.sm} />
              </Button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
