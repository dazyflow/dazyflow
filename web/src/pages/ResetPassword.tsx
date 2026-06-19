import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";

// ResetPassword is the landing page for the emailed reset link
// (/reset-password?email=…&token=…). The token in the link is the proof,
// so it works in any browser, signed in or not. On success the server
// has already revoked every existing session for the account, so the
// only way forward is a fresh sign-in — which is exactly what we offer.
export function ResetPassword() {
  const { t } = useTranslation();
  const [params] = useSearchParams();
  const email = params.get("email") ?? "";
  const resetToken = params.get("token") ?? "";

  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const badLink = !email || !resetToken;
  const mismatch = password !== "" && confirm !== "" && password !== confirm;

  if (badLink) {
    return (
      <div className="signin-wrap">
        <div className="signin">
          <h1>{t("resetPassword.badLinkTitle")}</h1>
          <p className="sub">{t("resetPassword.badLinkBody")}</p>
          <div className="signin-alt">
            <Link to="/forgot-password">{t("resetPassword.requestNew")}</Link>
          </div>
        </div>
      </div>
    );
  }

  if (done) {
    return (
      <div className="signin-wrap">
        <div className="signin">
          <h1>{t("resetPassword.doneTitle")}</h1>
          <p className="sub">{t("resetPassword.doneBody")}</p>
          <div className="signin-alt">
            <Link to={`/signin?email=${encodeURIComponent(email)}`}>
              {t("resetPassword.toSignin")}
            </Link>
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
          setErr(null);
          if (password.length < 8) {
            setErr(t("resetPassword.tooShort"));
            return;
          }
          if (password !== confirm) {
            setErr(t("resetPassword.mismatch"));
            return;
          }
          setBusy(true);
          try {
            await api.resetPassword(email, resetToken, password);
            setDone(true);
          } catch (e) {
            setErr(e instanceof APIError ? e.message : (e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <h1>{t("resetPassword.title")}</h1>
        <p className="sub">{t("resetPassword.subtitle", { email })}</p>
        {/* Email is fixed by the link; surface it read-only so the user
            sees which account they're resetting but can't retarget it. */}
        <label htmlFor="email">{t("resetPassword.email")}</label>
        <input id="email" type="email" value={email} readOnly autoComplete="username" />
        <label htmlFor="password">{t("resetPassword.newPassword")}</label>
        <input
          id="password"
          type="password"
          autoComplete="new-password"
          autoFocus
          minLength={8}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          aria-describedby="reset-password-hint"
        />
        <div id="reset-password-hint" className="signup-hint">
          {t("resetPassword.hint")}
        </div>
        <label htmlFor="confirm">{t("resetPassword.confirm")}</label>
        <input
          id="confirm"
          type="password"
          autoComplete="new-password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
        />
        <button
          type="submit"
          className="primary"
          disabled={busy || !password || !confirm || mismatch}
        >
          {busy ? t("resetPassword.submitting") : t("resetPassword.submit")}
        </button>
        {err && <div className="error">{err}</div>}
        <div className="signin-alt">
          <Link to="/signin">{t("resetPassword.backToSignin")}</Link>
        </div>
      </form>
    </div>
  );
}
