// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { AuthLayout } from "../../components/auth/AuthLayout";
import { api } from "../../api";
import { explainApiError } from "../../lib/explainApiError";

// ForgotPassword is the "email me a reset link" form. The server is
// deliberately non-enumerating — it returns the same success whether or
// not the address has an account — so any success shows the same "check
// your inbox" confirmation rather than revealing existence; only a request
// that never got that answer (network, 429, 5xx) shows an error. The
// ?email= deep link pre-fills the field (e.g. from the sign-in form).
export function ForgotPassword() {
  const { t } = useTranslation();
  const [searchParams] = useSearchParams();
  const [email, setEmail] = useState(searchParams.get("email") ?? "");
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  if (sent) {
    return (
      <AuthLayout>
        <div className="signin">
          <h1>{t("forgotPassword.sentTitle")}</h1>
          <p className="sub">{t("forgotPassword.sentBody", { email })}</p>
          <div className="signin-alt">
            <Link to="/signin">{t("forgotPassword.backToSignin")}</Link>
          </div>
        </div>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout>
      <form
        className="signin"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!email.trim()) return;
          setBusy(true);
          setErr(null);
          try {
            // Any 2xx is the uniform answer, account or not; only a failure to
            // get that answer (network, rate limit, server error) is reported —
            // "check your inbox" after one would promise a mail never sent.
            await api.requestPasswordReset(email.trim());
            setSent(true);
          } catch (e) {
            setErr(explainApiError(e, t));
          } finally {
            setBusy(false);
          }
        }}
      >
        <h1>{t("forgotPassword.title")}</h1>
        <p className="sub">{t("forgotPassword.subtitle")}</p>
        <label htmlFor="email">{t("common.email")}</label>
        <input
          id="email"
          type="email"
          autoComplete="username"
          autoFocus
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
        <Button type="submit" variant="primary" disabled={busy || !email.trim()}>
          {busy ? t("common.sending") : t("forgotPassword.submit")}
        </Button>
        {err && <div className="error">{err}</div>}
        <div className="signin-alt">
          <Link to="/signin">{t("forgotPassword.backToSignin")}</Link>
        </div>
      </form>
    </AuthLayout>
  );
}
