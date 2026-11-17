// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useId, type CSSProperties, type FormEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useEscapeToClose } from "./useEscapeToClose";

// Modal is the one dialog shell: backdrop, panel, portal, Escape, ARIA.
//
// The shell used to be pasted into every dialog, and the copies drifted: about
// half did not portal, a handful had no accessible name, and the "don't close
// while saving" guard was rewritten per dialog. Everything that has to be true
// of every dialog now lives here, and scripts/check-modal-a11y.mjs fails the
// build if `modal-backdrop` is rendered anywhere else.
//
//  - Portal to <body>. The backdrop is position:fixed, but a caller deep in the
//    tree (e.g. the inspector panel) may sit under a transformed /
//    container-query ancestor that would trap the fixed element inside that
//    subtree instead of the viewport.
//  - Escape and a backdrop click both call onClose — unless `dismissible` is
//    false (typically `dismissible={!busy}` while a request is in flight).
//  - The panel always has an accessible name. Pass `title` to get the standard
//    `.modal-head` with an <h2> that labels the dialog (and `headActions` for a
//    close button beside it); pass `labelledBy` when the caller renders its own
//    heading; or `ariaLabel` when there is no visible heading at all. The type
//    requires exactly one of them.
//
// `as="form"` makes the panel itself the <form> so Enter submits and the
// footer's submit button works without a nested form.

type Name =
  | { title: ReactNode; headActions?: ReactNode; labelledBy?: never; ariaLabel?: never }
  | { labelledBy: string; title?: never; headActions?: never; ariaLabel?: never }
  | { ariaLabel: string; title?: never; headActions?: never; labelledBy?: never };

export type ModalProps = Name & {
  onClose: () => void;
  /** False blocks Escape and backdrop-click dismissal (e.g. while busy). */
  dismissible?: boolean;
  /** Max width of the panel; omit for the default .modal width. */
  size?: "sm" | "md" | "lg";
  role?: "dialog" | "alertdialog";
  className?: string;
  style?: CSSProperties;
  as?: "div" | "form";
  onSubmit?: (e: FormEvent<HTMLFormElement>) => void;
  children: ReactNode;
};

export function Modal(props: ModalProps) {
  const {
    onClose,
    dismissible = true,
    size,
    role = "dialog",
    className,
    style,
    as = "div",
    onSubmit,
    children,
  } = props;
  const titleId = useId();
  const dismiss = () => {
    if (dismissible) onClose();
  };
  useEscapeToClose(dismiss);

  const hasTitle = props.title !== undefined;
  const panelProps = {
    className:
      "modal" + (size ? ` modal-${size}` : "") + (className ? ` ${className}` : ""),
    style,
    onClick: (e: React.MouseEvent) => e.stopPropagation(),
    role,
    "aria-modal": true as const,
    "aria-labelledby": hasTitle ? titleId : props.labelledBy,
    "aria-label": props.ariaLabel,
  };
  const content = (
    <>
      {hasTitle && (
        <div className="modal-head">
          <h2 id={titleId}>{props.title}</h2>
          {props.headActions}
        </div>
      )}
      {children}
    </>
  );

  return createPortal(
    <div className="modal-backdrop" onClick={dismiss}>
      {as === "form" ? (
        <form {...panelProps} onSubmit={onSubmit}>
          {content}
        </form>
      ) : (
        <div {...panelProps}>{content}</div>
      )}
    </div>,
    document.body,
  );
}
