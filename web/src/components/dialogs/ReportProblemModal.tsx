// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api } from "../../api";
import { Button } from "../ui/Button";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../ui/ErrorNotice";
import { ICON } from "../../icons";
import { useEscapeToClose } from "../ui/useEscapeToClose";

// ReportProblemModal files a support ticket about a specific flow/run — the
// zero-friction "the ask" path (Tier 1): the server auto-attaches a redacted
// diagnostic bundle for the referenced flow/run, so support can help without a
// live grant. On success it navigates to the new ticket's thread. Rendered from
// the run-failure banner when the deployment has the ticket surface enabled.
//
// defaultMessage seeds the message box with what the caller already knows —
// the error the user is looking at. The bundle carries the same facts in
// machine form, but a ticket whose first line is blank asks the user to
// retype an error that is on screen in front of them, and most people send it
// empty instead. Editable, not fixed: it is a starting point for "and here is
// what I was doing", which is the part only they can supply.
export function ReportProblemModal({
  flowId,
  runId,
  flowName,
  defaultMessage,
  onClose,
}: {
  flowId?: string;
  runId?: string;
  flowName?: string;
  defaultMessage?: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const navigate = useNavigate();
  const [subject, setSubject] = useState(
    flowName
      ? t("report.defaultSubject", { flow: flowName })
      : "",
  );
  const [message, setMessage] = useState(defaultMessage ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    if (!token || !subject.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      const tk = await api.createTicket(token, {
        subject: subject.trim(),
        flow_id: flowId,
        run_id: runId,
        message: message.trim() || undefined,
      });
      navigate(`/support/${encodeURIComponent(tk.id)}`);
    } catch (e) {
      setErr(explainApiError(e, t));
      setBusy(false);
    }
  };

  useEscapeToClose(onClose);

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        aria-modal="true"
        role="dialog"
        aria-label={t("report.title")}
        style={{ maxWidth: 520 }}
      >
        <div className="modal-head">
          <strong>{t("report.title")}</strong>
          <Button size="icon" onClick={onClose} aria-label={t("common.close")}>
            <X size={ICON.md} />
          </Button>
        </div>
        <div className="modal-body">
          <p className="muted" style={{ fontSize: "var(--text-sm)", margin: "0 0 var(--space-3)" }}>
            {t("report.lede")}
          </p>
          <label className="field-label">{t("support.subjectLabel")}</label>
          <input
            className="input"
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            autoFocus
          />
          <label className="field-label" style={{ marginTop: "var(--space-3)" }}>
            {t("support.messageLabel")}
          </label>
          <textarea
            className="input"
            rows={5}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder={t("report.messagePlaceholder")}
          />
          {err && <ErrorNotice style={{ marginTop: "var(--space-3)" }}>{err}</ErrorNotice>}
        </div>
        <div className="modal-foot">
          <Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || !subject.trim()}>
            {t("report.send")}
          </Button>
        </div>
      </div>
    </div>
  );
}
