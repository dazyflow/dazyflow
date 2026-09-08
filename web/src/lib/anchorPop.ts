// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later


export type AnchorRect = {
  top: number;
  bottom: number;
  left: number;
  right: number;
};

export type AnchorSize = { width: number; height: number };

export type AnchorPos = { left: number; top: number };

export type AnchorOpts = {
  align?: "left" | "right";
  gap?: number;
  margin?: number;
  viewport?: AnchorSize;
};

// anchorBelow places `size` under `trigger`, flipping above when there is no
// room below and clamping to the viewport on both axes, so a panel is never
// rendered half off-screen. Returns viewport coordinates for position:fixed.
export function anchorBelow(
  trigger: AnchorRect,
  size: AnchorSize,
  opts: AnchorOpts = {},
): AnchorPos {
  const align = opts.align ?? "right";
  const gap = opts.gap ?? 6;
  const margin = opts.margin ?? 8;
  const vw = opts.viewport?.width ?? window.innerWidth;
  const vh = opts.viewport?.height ?? window.innerHeight;

  // Vertical: below by preference. Flip above only when below overflows AND
  // above actually fits — flipping into a second overflow trades one clipped
  // panel for another, and below-then-clamped at least keeps the top visible.
  let top = trigger.bottom + gap;
  const fitsBelow = top + size.height <= vh - margin;
  const above = trigger.top - gap - size.height;
  if (!fitsBelow && above >= margin) top = above;

  let left = align === "right" ? trigger.right - size.width : trigger.left;
  left = Math.min(left, vw - margin - size.width);
  left = Math.max(margin, left);

  top = Math.min(top, vh - margin - size.height);
  top = Math.max(margin, top);

  return { left, top };
}
