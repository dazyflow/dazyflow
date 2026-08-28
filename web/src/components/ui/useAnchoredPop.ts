// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { anchorBelow, type AnchorOpts, type AnchorPos } from "../../lib/anchorPop";

// Positions a portaled popover under its trigger and keeps it on screen.
//
// The panel has to be in the DOM before it can be measured, so the caller
// renders it hidden on the first frame and visible once `pos` arrives — see
// `style` below, which packages that. One frame of hidden-but-laid-out is
// invisible to the user and cheaper than duplicating the panel's max-width
// rules in JS to predict its size.
export function useAnchoredPop<T extends HTMLElement, P extends HTMLElement>(
  open: boolean,
  opts: AnchorOpts = {},
) {
  const trigger = useRef<T | null>(null);
  const pop = useRef<P | null>(null);
  const [pos, setPos] = useState<AnchorPos | null>(null);
  // Held in a ref so a caller passing an inline literal (all of them) doesn't
  // re-register the reflow listeners on every render.
  const optsRef = useRef(opts);
  optsRef.current = opts;

  const place = useCallback(() => {
    const t = trigger.current?.getBoundingClientRect();
    const p = pop.current?.getBoundingClientRect();
    if (!t || !p) return;
    setPos(anchorBelow(t, { width: p.width, height: p.height }, optsRef.current));
  }, []);

  useLayoutEffect(() => {
    if (!open) {
      // Drop the stale coords so the next open measures again rather than
      // flashing the panel at wherever the trigger used to be.
      setPos(null);
      return;
    }
    place();
    // Capture on scroll: the trigger usually sits in a scrolling panel
    // (.inspector-body), whose scroll events don't reach window otherwise.
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [open, place]);

  const style: React.CSSProperties = {
    position: "fixed",
    left: pos?.left ?? 0,
    top: pos?.top ?? 0,
    right: "auto",
    bottom: "auto",
    visibility: pos ? "visible" : "hidden",
  };

  return { trigger, pop, pos, style, place };
}
