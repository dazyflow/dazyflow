// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { api } from "../../api";

// ForgotPassword is the "email me a reset link" form. The server is
// deliberately non-enumerating — it returns the same success whether or
// not the address has an account — so on submit we always show the same
// "check your inbox" confirmation rather than revealing existence. The
// ?email= deep link pre-fills the field (e.g. from the sign-in form).
export function ForgotPassword() {
  const { t } = useTranslation();
  const [searchParams] = useSearchParams();
  const [email, setEmail] = useState(searchParams.get("email") ?? "");
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);

  if (sent) {
    return (
      <div className="signin-wrap">
        <div className="signin">
          <h1>{t("forgotPassword.sentTitle")}</h1>
          <p className="sub">{t("forgotPassword.sentBody", { email })}</p>
          <div className="signin-alt">
            <Link to="/signin">{t("forgotPassword.backToSignin")}</Link>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="signin-wrap">
      <form
        className="signin"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!email.trim()) return;
          setBusy(true);
          try {
            await api.requestPasswordReset(email.trim());
          } catch {
            // Non-enumerating + best-effort: even a network error
            // shouldn't reveal anything, so we show the same confirmation.
          } finally {
            setBusy(false);
            setSent(true);
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
        <div className="signin-alt">
          <Link to="/signin">{t("forgotPassword.backToSignin")}</Link>
        </div>
      </form>
    </div>
  );
}
