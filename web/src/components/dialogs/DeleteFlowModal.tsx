// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { APIError } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { Button } from "../ui/Button";
import { ErrorNotice } from "../ui/ErrorNotice";
import { useEscapeToClose } from "../ui/useEscapeToClose";

// DeleteFlowModal is the password-gated confirm dialog for deleting a flow.
// Deletion is irreversible (it drops the flow's whole history), so — like
// the daemon's other high-consequence ops — it requires re-entering the
// account password, which the server re-verifies. Used from both the flow
// list (per-card delete) and the editor's Settings → General danger zone.
//
// The parent owns the actual delete + any follow-up (refresh the list /
// navigate away from the now-gone editor) via onConfirm(password). That
// promise rejecting keeps the dialog open and surfaces the reason; a 401
// reads as "wrong password" and a 409 as "stop the run first". On success
// the parent's onClose tears the dialog down.
export function DeleteFlowModal({
  flowName,
  onConfirm,
  onClose,
}: {
  flowName: string;
  onConfirm: (password: string) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Escape cancels — but not mid-delete, so a stray keypress can't abandon
  // the dialog while the request is in flight.
  useEscapeToClose(() => !busy && onClose());

  const submit = async () => {
    if (!password || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await onConfirm(password);
      // Success: the parent navigates/refreshes and unmounts us — nothing
      // more to do here.
    } catch (e) {
      const status = e instanceof APIError ? e.status : 0;
      setErr(
        status === 401
          ? t("deleteFlow.wrongPassword")
          : status === 409
            ? t("deleteFlow.locked")
            : explainApiError(e, t),
      );
      setBusy(false);
    }
  };

  return createPortal(
    <div className="modal-backdrop" onClick={() => !busy && onClose()}>
      <div
        className="modal confirm-dialog"
        onClick={(e) => e.stopPropagation()}
        role="alertdialog"
        aria-modal="true"
      >
        <div className="modal-head">
          <h2>{t("deleteFlow.title")}</h2>
        </div>
        <div className="modal-body">
          <p className="confirm-message">
            {t("deleteFlow.body", { name: flowName })}
          </p>
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="delete-flow-password">
                {t("deleteFlow.passwordLabel")}
              </label>
            </div>
            <input
              id="delete-flow-password"
              type="password"
              autoFocus
              autoComplete="current-password"
              value={password}
              disabled={busy}
              placeholder={t("deleteFlow.passwordPlaceholder")}
              onChange={(e) => setPassword(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") submit();
              }}
            />
          </div>
          {err && <ErrorNotice>{err}</ErrorNotice>}
        </div>
        <div className="modal-foot">
          <Button onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" onClick={submit} disabled={busy || !password}>
            {busy ? t("deleteFlow.deleting") : t("common.deleteFlow")}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
