// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { Button } from "./Button";
import { explainApiError } from "../lib/explainApiError";

// ReportProblemModal files a support ticket about a specific flow/run — the
// zero-friction "the ask" path (Tier 1): the server auto-attaches a redacted
// diagnostic bundle for the referenced flow/run, so support can help without a
// live grant. On success it navigates to the new ticket's thread. Rendered from
// the run-failure banner when the deployment has the ticket surface enabled.
export function ReportProblemModal({
  flowId,
  runId,
  flowName,
  onClose,
}: {
  flowId?: string;
  runId?: string;
  flowName?: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const navigate = useNavigate();
  const [subject, setSubject] = useState(
    flowName
      ? t("report.defaultSubject", { defaultValue: 'Problem with "{{flow}}"', flow: flowName })
      : "",
  );
  const [message, setMessage] = useState("");
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

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 520 }}>
        <h2>{t("report.title", { defaultValue: "Report a problem" })}</h2>
        <p style={{ color: "var(--muted)", fontSize: "var(--text-sm)", marginTop: 0 }}>
          {t("report.lede", {
            defaultValue:
              "We'll attach a diagnostic snapshot of this flow — its structure and the error, with your secrets and data removed.",
          })}
        </p>
        <label className="field-label">{t("support.subjectLabel", { defaultValue: "Subject" })}</label>
        <input
          className="input"
          value={subject}
          onChange={(e) => setSubject(e.target.value)}
          autoFocus
        />
        <label className="field-label" style={{ marginTop: "var(--space-3)" }}>
          {t("support.messageLabel", { defaultValue: "Details" })}
        </label>
        <textarea
          className="input"
          rows={5}
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          placeholder={t("report.messagePlaceholder", {
            defaultValue: "What were you trying to do when it failed?",
          })}
        />
        {err && <div className="card error" style={{ marginTop: "var(--space-2)" }}>{err}</div>}
        <div className="modal-actions">
          <Button onClick={onClose}>{t("common.cancel")}</Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || !subject.trim()}>
            {t("report.send", { defaultValue: "Send to support" })}
          </Button>
        </div>
      </div>
    </div>
  );
}
