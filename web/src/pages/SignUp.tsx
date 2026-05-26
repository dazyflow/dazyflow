import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";

// SignUp is the self-serve account creation page. It mirrors the
// SignIn layout closely on purpose — the two pages should feel like
// tabs of the same form, not two different experiences.
//
// Confirm-password field is local-only validation; we don't ship it
// to the server. The server enforces length-only (min 8); the form
// adds a "passwords match" check so users catch a typo before the
// round-trip.
export function SignUp() {
  const { t } = useTranslation();
  const { signUpWithPassword, error, loading } = useAuth();
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [localErr, setLocalErr] = useState<string | null>(null);

  const passwordMismatch = password !== "" && confirm !== "" && password !== confirm;

  return (
    <div className="signin-wrap">
      <form
        className="signin"
        onSubmit={async (e) => {
          e.preventDefault();
          setLocalErr(null);
          if (!email.trim() || !password) return;
          if (password.length < 8) {
            setLocalErr(t("signUp.tooShort"));
            return;
          }
          if (password !== confirm) {
            setLocalErr(t("signUp.mismatch"));
            return;
          }
          setBusy(true);
          try {
            await signUpWithPassword(email.trim(), password);
            // After signup, send the user to the welcome wizard.
            navigate("/welcome");
          } catch {
            /* server error already set on context.error */
          } finally {
            setBusy(false);
          }
        }}
      >
        <h1>{t("signUp.title")}</h1>
        <label htmlFor="email">{t("signUp.email")}</label>
        <input
          id="email"
          type="email"
          autoComplete="username"
          autoFocus
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
        <label htmlFor="password">{t("signUp.password")}</label>
        <input
          id="password"
          type="password"
          autoComplete="new-password"
          minLength={8}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder={t("signUp.passwordPlaceholder")}
        />
        <label htmlFor="confirm">{t("signUp.confirm")}</label>
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
          disabled={
            busy || loading || !email.trim() || !password || passwordMismatch
          }
        >
          {busy ? t("signUp.submitting") : t("signUp.submit")}
        </button>
        {(localErr || error) && <div className="error">{localErr ?? error}</div>}
        <div className="signin-alt">
          {t("signUp.haveAccount")} <Link to="/signin">{t("signUp.signInLink")}</Link>
        </div>
      </form>
    </div>
  );
}
