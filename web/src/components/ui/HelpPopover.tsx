// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useId, useState } from "react";
import { createPortal } from "react-dom";
import { Info } from "lucide-react";
import { ICON } from "../../icons";
import { useAnchoredPop } from "./useAnchoredPop";

// The (i) affordance on a step header or a form field.
//
// A native `title=` tooltip failed the readers who needed it most: it does not
// fire on touch at all, so on a tablet every word of the step and field guidance
// was absent, and it cannot be scrolled or pinned, which made it the wrong
// container for a drop description (median 63 words). Click-to-open fixes both.
//
// `label` stays on the button as its accessible name and hover text, so the
// affordance still says what it is before you open it. The body deliberately
// does NOT also live in `title`: two copies of the same prose read as a
// duplicate to a screen reader and flicker a native tooltip over the panel.
//
// The panel is portaled to <body> and positioned from the trigger's rect
// (useAnchoredPop), because both call sites live inside .inspector-body, which
// scrolls — and a scroll container clips on both axes, slicing an
// absolutely-positioned panel off at its edge.
export function HelpPopover({ label, body }: { label: string; body: string }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const { trigger, pop, style } = useAnchoredPop<
    HTMLButtonElement,
    HTMLSpanElement
  >(open);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      setOpen(false);
      trigger.current?.focus();
    };
    const onDown = (e: PointerEvent) => {
      const target = e.target as Node;
      // The panel is portaled, so it is not a descendant of the trigger:
      // both have to be checked or scrolling the prose would dismiss it.
      if (trigger.current?.contains(target)) return;
      if (pop.current?.contains(target)) return;
      setOpen(false);
    };
    // Capture on both: the canvas and the inspector stop propagation on their
    // own handlers, so a bubbling listener never sees the click that should
    // dismiss this.
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("pointerdown", onDown, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("pointerdown", onDown, true);
    };
  }, [open, trigger, pop]);

  return (
    <span className="help-pop-wrap">
      <button
        ref={trigger}
        type="button"
        className="inspector-info"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        aria-label={label}
        title={label}
        onClick={(e) => {
          e.stopPropagation();
          setOpen((v) => !v);
        }}
      >
        <Info size={ICON.sm} aria-hidden="true" />
      </button>
      {open &&
        createPortal(
          <span className="help-pop" id={id} role="note" ref={pop} style={style}>
            {body}
          </span>,
          document.body,
        )}
    </span>
  );
}
