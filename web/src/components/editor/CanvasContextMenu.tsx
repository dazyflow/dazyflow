// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef } from "react";
import { Check } from "lucide-react";
import { ICON } from "../../icons";

export type ContextMenuItem =
  | { separator: true }
  | { header: string }
  | {
      label: string;
      onClick: () => void;
      danger?: boolean;
      disabled?: boolean;
      checked?: boolean;
      title?: string;
      shortcut?: string;
    };

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
      onClick={(e) => e.stopPropagation()}
      onContextMenu={(e) => e.preventDefault()}
    >
      {items.map((it, i) =>
        "separator" in it ? (
          <div key={i} className="context-menu-sep" role="separator" />
        ) : "header" in it ? (
          <div key={i} className="context-menu-head" role="presentation">
            {it.header}
          </div>
        ) : (
          <button
            key={i}
            type="button"
            role={it.checked === undefined ? "menuitem" : "menuitemradio"}
            aria-checked={it.checked === undefined ? undefined : it.checked}
            className={it.danger ? "danger" : ""}
            disabled={it.disabled}
            title={it.title}
            onClick={() => {
              it.onClick();
              onClose();
            }}
          >
            {/* The gutter is always rendered for a checkable item so the
                labels of a radio group line up with each other. */}
            {it.checked !== undefined && (
              <span className="context-menu-check" aria-hidden="true">
                {it.checked ? <Check size={ICON.xs} /> : null}
              </span>
            )}
            <span className="context-menu-label">{it.label}</span>
            {it.shortcut && <span className="context-menu-shortcut">{it.shortcut}</span>}
          </button>
        ),
      )}
    </div>
  );
}
