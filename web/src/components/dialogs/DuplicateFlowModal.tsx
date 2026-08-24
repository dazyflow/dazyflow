// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { explainApiError } from "../../lib/explainApiError";
import { Button } from "../ui/Button";
import { ErrorNotice } from "../ui/ErrorNotice";

// DuplicateFlowModal prompts for the new flow's name before copying. The
// field is pre-filled with "Copy of <source>" so the common case is a single
// Enter. The parent owns the actual duplicate + follow-up (open the copy in
// the editor) via onConfirm(name); that promise rejecting keeps the dialog
// open and surfaces the reason. On success the parent's onClose unmounts us.
export function DuplicateFlowModal({
  sourceName,
  defaultName,
  onConfirm,
  onClose,
}: {
  sourceName: string;
  defaultName: string;
  onConfirm: (name: string) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(defaultName);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Escape cancels — but not mid-flight, so a stray keypress can't abandon the
  // dialog while the request is running.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busy) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, busy]);

  const submit = async () => {
    const trimmed = name.trim();
    if (!trimmed || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await onConfirm(trimmed);
      // Success: the parent navigates to the copy and unmounts us.
    } catch (e) {
      setErr(explainApiError(e, t));
      setBusy(false);
    }
  };

  return createPortal(
    <div className="settings-backdrop" onClick={() => !busy && onClose()}>
      <div
        className="settings-dialog confirm-dialog"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <div className="settings-head">
          <h2>{t("duplicateFlow.title")}</h2>
        </div>
        <div className="settings-body">
          <p className="confirm-message">
            {t("duplicateFlow.body", { name: sourceName })}
          </p>
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="duplicate-flow-name">
                {t("duplicateFlow.nameLabel")}
              </label>
            </div>
            <input
              id="duplicate-flow-name"
              type="text"
              autoFocus
              value={name}
              disabled={busy}
              placeholder={t("duplicateFlow.namePlaceholder")}
              onChange={(e) => setName(e.target.value)}
              onFocus={(e) => e.target.select()}
              onKeyDown={(e) => {
                if (e.key === "Enter") submit();
              }}
            />
          </div>
          {err && <ErrorNotice>{err}</ErrorNotice>}
        </div>
        <div className="settings-foot">
          <Button onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            onClick={submit}
            disabled={busy || !name.trim()}
          >
            {busy ? t("duplicateFlow.duplicating") : t("duplicateFlow.confirm")}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
