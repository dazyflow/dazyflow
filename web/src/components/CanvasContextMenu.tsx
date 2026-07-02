// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef } from "react";

// CanvasContextMenu is the small right-click actions menu the flow editor pops
// over a node or edge (Blueprint-style). It's a dumb, positioned list: the
// editor builds the items (label + handler) and owns what each does. It closes
// itself on the next click, another right-click, Escape, scroll, or resize —
// the usual "click away and it's gone" contract.
export type ContextMenuItem =
  | { separator: true }
  | { label: string; onClick: () => void; danger?: boolean; disabled?: boolean };

export function CanvasContextMenu({
  x,
  y,
  items,
  onClose,
}: {
  x: number;
  y: number;
  items: ContextMenuItem[];
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const close = () => onClose();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    // Defer wiring the click-away listener to the next tick so the same
    // event burst that opened the menu doesn't immediately close it.
    const id = window.setTimeout(() => {
      window.addEventListener("click", close);
      window.addEventListener("contextmenu", close);
      window.addEventListener("resize", close);
      window.addEventListener("scroll", close, true);
      window.addEventListener("keydown", onKey);
    }, 0);
    return () => {
      window.clearTimeout(id);
      window.removeEventListener("click", close);
      window.removeEventListener("contextmenu", close);
      window.removeEventListener("resize", close);
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("keydown", onKey);
    };
  }, [onClose]);

  // Nudge the menu back on-screen if it would overflow the right/bottom edge.
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    if (r.right > window.innerWidth) el.style.left = `${Math.max(4, window.innerWidth - r.width - 4)}px`;
    if (r.bottom > window.innerHeight) el.style.top = `${Math.max(4, window.innerHeight - r.height - 4)}px`;
  }, [x, y]);

  return (
    <div
      ref={ref}
      className="canvas-context-menu"
      style={{ left: x, top: y }}
      role="menu"
      // Keep clicks inside the menu from bubbling to the window close listener.
      onClick={(e) => e.stopPropagation()}
      onContextMenu={(e) => e.preventDefault()}
    >
      {items.map((it, i) =>
        "separator" in it ? (
          <div key={i} className="context-menu-sep" role="separator" />
        ) : (
          <button
            key={i}
            type="button"
            role="menuitem"
            className={it.danger ? "danger" : ""}
            disabled={it.disabled}
            onClick={() => {
              it.onClick();
              onClose();
            }}
          >
            {it.label}
          </button>
        ),
      )}
    </div>
  );
}
