// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useId, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { CircleAlert, TriangleAlert } from "lucide-react";
import { ICON } from "../../icons";
import { useAnchoredPop } from "../ui/useAnchoredPop";

// The editor's two issue affordances: an Errors button and a Warnings button,
// each opening an anchored panel listing what it counts.
//
// These messages used to be banners stacked over the canvas, which put the
// flow's own steps behind a column of prose exactly when the author was working
// on them — a six-second explanation of a refused wire covered the wire it was
// about. The count on the button is the standing signal; the words are one click
// away.
//
// Controlled from the editor rather than self-managed, because opening is not
// always the user's doing: a refused connection opens the Warnings panel itself,
// and both panels close when their last row goes away.
export type IssueKind = "error" | "warning";

export function IssuesButton({
  kind,
  count,
  title,
  heading,
  open,
  onOpenChange,
  children,
}: {
  kind: IssueKind;
  count: number;
  title: string;
  heading: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  children: ReactNode;
}) {
  const id = useId();
  const { trigger, pop, style } = useAnchoredPop<
    HTMLButtonElement,
    HTMLDivElement
  >(open);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      onOpenChange(false);
      trigger.current?.focus();
    };
    const onDown = (e: PointerEvent) => {
      const target = e.target as Node;
      // Portaled, so the panel is not inside the trigger — both have to be
      // checked or clicking a row's own button would dismiss the panel.
      if (trigger.current?.contains(target)) return;
      if (pop.current?.contains(target)) return;
      onOpenChange(false);
    };
    // Capture on both: the canvas stops propagation on its own handlers, so a
    // bubbling listener never sees the click that should dismiss this.
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("pointerdown", onDown, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("pointerdown", onDown, true);
    };
  }, [open, onOpenChange, trigger, pop]);

  if (count <= 0) return null;
  const Icon = kind === "error" ? CircleAlert : TriangleAlert;

  return (
    <>
      <button
        ref={trigger}
        type="button"
        className="editor-issues-btn"
        data-kind={kind}
        title={title}
        aria-label={title}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={(e) => {
          e.stopPropagation();
          onOpenChange(!open);
        }}
      >
        <Icon size={ICON.sm} aria-hidden="true" />
        <span className="editor-issues-count">{count}</span>
      </button>
      {open &&
        createPortal(
          <div
            id={id}
            ref={pop}
            style={style}
            className="editor-issues-pop"
            data-kind={kind}
            role="dialog"
            aria-label={heading}
          >
            <div className="editor-issues-pop-head">{heading}</div>
            <div className="editor-issues-pop-body">{children}</div>
          </div>,
          document.body,
        )}
    </>
  );
}

export function IssueRow({
  text,
  actions,
  title,
}: {
  text: ReactNode;
  actions?: ReactNode;
  title?: string;
}) {
  return (
    <div className="editor-issues-row" title={title}>
      <div className="editor-issues-row-text">{text}</div>
      {actions && <div className="editor-issues-row-actions">{actions}</div>}
    </div>
  );
}
