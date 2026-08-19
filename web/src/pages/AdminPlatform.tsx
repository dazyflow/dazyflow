// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { AlertCircle, Check, Mail, Send } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { Button } from "../components/Button";
import { ErrorNotice } from "../components/ErrorNotice";

// AdminPlatform is the platform-operator email surface: a single place to
// confirm the instance mailer actually delivers. (Signup invites — the
// other thing that used to live here — moved to /admin/platform/users,
// next to the account roster they grow.)
export function AdminPlatform() {
  const { t } = useTranslation();
  const { hasPerm } = useAuth();
  if (!hasPerm("platform:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.platform.needPlatformAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }
  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Mail size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.platform.title")}
          </h1>
          <div className="sub">{t("admin.platform.subtitle")}</div>
        </div>
      </div>
      <SmtpTestSection />
    </div>
  );
}

// SmtpTestSection lets a platform admin fire one test message through the
// instance mailer (DAZYFLOW_SMTP_URL). Boot only checks that the URL
// parses; this is the only way to confirm the server actually accepts
// mail. Leave the field blank to send to yourself.
function SmtpTestSection() {
  const { t } = useTranslation();
  const { token, me } = useAuth();
  const [to, setTo] = useState("");
  const [sending, setSending] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(
    null,
  );

  const trimmed = to.trim();
  const looksValid = trimmed === "" || EMAIL_RE.test(trimmed);
  const canSend = !sending && looksValid;

  const send = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || !canSend) return;
    setSending(true);
    setResult(null);
    try {
      const r = await api.smtpTest(token, trimmed || undefined);
      setResult({ ok: true, msg: t("admin.smtpTest.sent", { to: r.to }) });
    } catch (e) {
      setResult({ ok: false, msg: explainApiError(e, t) });
    } finally {
      setSending(false);
    }
  };

  return (
    <div style={{ marginTop: "var(--space-5)" }}>
      <h2 className="admin-section-head">{t("admin.smtpTest.head")}</h2>
      <div className="sub" style={{ marginBottom: "var(--space-3)" }}>
        {t("admin.smtpTest.subtitle")}
      </div>
      <form
        onSubmit={send}
        style={{ display: "flex", gap: "var(--space-2)", alignItems: "flex-start" }}
      >
        <input
          type="email"
          value={to}
          onChange={(e) => setTo(e.target.value)}
          placeholder={me?.subject ?? t("admin.smtpTest.recipientPlaceholder")}
          aria-label={t("admin.smtpTest.recipientLabel")}
          style={{ flex: 1 }}
        />
        <Button type="submit" variant="primary" disabled={!canSend}>
          <Send size={14} style={{ marginRight: 6 }} />
          {sending ? t("admin.smtpTest.sending") : t("admin.smtpTest.send")}
        </Button>
      </form>
      {!looksValid && (
        <div className="sub" style={{ color: "var(--danger)", marginTop: "var(--space-1)" }}>
          {t("admin.smtpTest.invalid")}
        </div>
      )}
      {result && (
        <div
          className={result.ok ? "card" : "card error"}
          style={{
            marginTop: "var(--space-2)",
            ...(result.ok ? { color: "var(--success, var(--accent))" } : {}),
          }}
        >
          {result.ok ? (
            <Check size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          ) : (
            <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          )}
          {result.msg}
        </div>
      )}
    </div>
  );
}

// EMAIL_RE is intentionally permissive — full validation is the server's
// job. We just catch obvious typos before a round-trip.
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
