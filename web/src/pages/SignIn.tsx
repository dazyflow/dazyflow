import { useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";

export function SignIn() {
  const { t } = useTranslation();
  const { signInWithPassword, error, loading } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  return (
    <div className="signin-wrap">
      <form
        className="signin"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!email.trim() || !password) return;
          setBusy(true);
          try {
            await signInWithPassword(email.trim(), password);
          } catch {
            /* error already set on context */
          } finally {
            setBusy(false);
          }
        }}
      >
        <h1>{t("signIn.title")}</h1>
        <label htmlFor="email">{t("signIn.email")}</label>
        <input
          id="email"
          type="email"
          autoComplete="username"
          autoFocus
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
        <label htmlFor="password">{t("signIn.password")}</label>
        <input
          id="password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <button
          type="submit"
          className="primary"
          disabled={busy || loading || !email.trim() || !password}
        >
          {busy ? t("signIn.submitting") : t("signIn.submit")}
        </button>
        {error && <div className="error">{error}</div>}
        <div className="signin-alt">
          {t("signIn.newHere")} <Link to="/signup">{t("signIn.createAccount")}</Link>
        </div>
      </form>
    </div>
  );
}
