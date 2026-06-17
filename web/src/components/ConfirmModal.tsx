import { useEffect, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

// ConfirmModal is the app's reusable "are you sure?" dialog — a themed
// replacement for window.confirm(). Use it for any destructive action
// that needs a deliberate yes/no (revoking a webhook key, deleting a
// step, …) instead of the browser's unstyled, untranslatable prompt.
//
// Mount it conditionally while a confirmation is pending; it resolves
// through onConfirm / onCancel. Cancel is the safe default: it's
// autofocused, and Escape or a backdrop click both cancel. Set `danger`
// for destructive confirms so the action button reads as a warning.
export function ConfirmModal({
  title,
  message,
  confirmLabel,
  cancelLabel,
  danger,
  confirmDisabled,
  onConfirm,
  onCancel,
}: {
  title: string;
  message: ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  danger?: boolean;
  // confirmDisabled blocks the confirm action (e.g. until a required field is
  // filled). The dialog can still be cancelled.
  confirmDisabled?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);
  // Portal to <body>: the backdrop is position:fixed, but a caller deep
  // in the tree (e.g. the inspector panel) may sit under a transformed /
  // container-query ancestor that would trap the fixed element inside
  // that subtree instead of the viewport. Rendering at the body level
  // makes the modal correct no matter where it's used.
  return createPortal(
    <div className="settings-backdrop" onClick={onCancel}>
      <div
        className="settings-dialog confirm-dialog"
        onClick={(e) => e.stopPropagation()}
        role="alertdialog"
        aria-modal="true"
      >
        <div className="settings-head">
          <h2>{title}</h2>
        </div>
        <div className="settings-body">
          <p className="confirm-message">{message}</p>
        </div>
        <div className="settings-foot">
          {/* Cancel is the safe default — autofocused so a reflexive
              Enter/Space dismisses rather than confirms a destructive act. */}
          <button type="button" onClick={onCancel} autoFocus>
            {cancelLabel ?? t("common.cancel")}
          </button>
          <button
            type="button"
            className={danger ? "danger" : "primary"}
            onClick={onConfirm}
            disabled={confirmDisabled}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
