// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { FormEvent, ReactNode } from "react";
import { createPortal } from "react-dom";
import { useEscapeToClose } from "./useEscapeToClose";

// Modal is the one dialog shell: backdrop, panel, ARIA, Escape, portal.
//
// It used to be pasted into ~30 components, and the copies drifted: about a
// dozen did not portal (so a transformed / container-query ancestor could trap
// the position:fixed backdrop inside its subtree instead of the viewport),
// several had no accessible name, and the "don't close mid-save" guard was
// reimplemented by hand five times. Each of those is now a property of the
// shell rather than something each call site has to remember:
//
//   - always portaled to <body>;
//   - Escape and a backdrop click both call onClose — unless `dismissible` is
//     false (pass `!busy` to keep a save from being abandoned half-way);
//   - an accessible name is required by the type: `title` (aria-label) or
//     `labelledBy` (the id of a heading inside the dialog).
//
// The head/body/foot markup stays with the caller (`modal-head`, `modal-body`,
// `modal-foot`): dialogs differ too much there for one layout to fit. Pass
// `onSubmit` to render the panel as a <form> so Enter submits.

type AccessibleName =
  | { title: string; labelledBy?: never }
  | { labelledBy: string; title?: never };

export type ModalProps = AccessibleName & {
  onClose: () => void;
  /** When false, Escape and backdrop clicks are ignored. Default true. */
  dismissible?: boolean;
  /** Max width of the panel. Omit for the default (640px). */
  size?: "sm" | "md" | "lg";
  /** Extra classes for the panel, e.g. "confirm-dialog". */
  className?: string;
  /** "alertdialog" for confirms that interrupt the user. Default "dialog". */
  role?: "dialog" | "alertdialog";
  /** Render the panel as a <form> with this submit handler. */
  onSubmit?: (e: FormEvent<HTMLFormElement>) => void;
  children: ReactNode;
};

// Literal class names so the CSS class checker can see them.
const SIZE_CLASS = {
  sm: "modal-sm",
  md: "modal-md",
  lg: "modal-lg",
} as const;

export function Modal({
  title,
  labelledBy,
  onClose,
  dismissible = true,
  size,
  className,
  role = "dialog",
  onSubmit,
  children,
}: ModalProps) {
  const dismiss = () => {
    if (dismissible) onClose();
  };
  useEscapeToClose(dismiss);

  const panelClass = ["modal", size && SIZE_CLASS[size], className]
    .filter(Boolean)
    .join(" ");
  const panelProps = {
    className: panelClass,
    onClick: (e: { stopPropagation: () => void }) => e.stopPropagation(),
    role,
    "aria-modal": true,
    "aria-label": title,
    "aria-labelledby": labelledBy,
  } as const;

  // Portal to <body>: the backdrop is position:fixed, but a caller deep in the
  // tree (the inspector, a canvas node) may sit under a transformed or
  // container-query ancestor that would trap the fixed element inside that
  // subtree — clipped to it and scaled by the canvas zoom.
  return createPortal(
    <div className="modal-backdrop" onClick={dismiss}>
      {onSubmit ? (
        <form {...panelProps} onSubmit={onSubmit}>
          {children}
        </form>
      ) : (
        <div {...panelProps}>{children}</div>
      )}
    </div>,
    document.body,
  );
}
