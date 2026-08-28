// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Placement maths for panels anchored to a control — dropdown menus, the (i)
// help popover — that are rendered in a portal at <body> with position:fixed.
//
// Anchoring such a panel with plain CSS (position:absolute; top:100%; right:0)
// fails in two ways this module exists to avoid. It is clipped by any scrolling
// or transformed ancestor — .inspector-body scrolls, so a help popover near the
// panel's edge was cut in half — and it is painted inside the ancestor's
// stacking context, so the topbar's three-dots menu (topbar z-index 20) lost to
// the narrow-width inspector sheet (30) no matter what z-index the menu itself
// carried. A portal escapes both, and in exchange the caller owns the
// coordinates: hence this.

export type AnchorRect = {
  top: number;
  bottom: number;
  left: number;
  right: number;
};

export type AnchorSize = { width: number; height: number };

export type AnchorPos = { left: number; top: number };

export type AnchorOpts = {
  // Which edge of the panel lines up with the same edge of the trigger.
  // "right" is the usual choice for a control near the right of the screen.
  align?: "left" | "right";
  // Gap between trigger and panel.
  gap?: number;
  // Keep-out margin from the viewport edges.
  margin?: number;
  // Viewport, injectable so this is testable without a DOM.
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

  // Horizontal: align the chosen edge, then pull back inside the viewport.
  // max() last so a panel wider than the viewport starts at the left margin
  // rather than being pushed off the left edge by the right-hand clamp.
  let left = align === "right" ? trigger.right - size.width : trigger.left;
  left = Math.min(left, vw - margin - size.width);
  left = Math.max(margin, left);

  // Clamp the vertical too, for a trigger that is itself partly off-screen.
  top = Math.min(top, vh - margin - size.height);
  top = Math.max(margin, top);

  return { left, top };
}
