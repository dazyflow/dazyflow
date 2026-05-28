import { useEffect, useState } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";

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
  const [searchParams] = useSearchParams();
  // Pre-fill from /invite-flow deep links so the recipient doesn't
  // have to retype the email the invitation was sent to. The invite
  // token rides through to the post-signup redirect so they land
  // directly on the accept page.
  const presetEmail = searchParams.get("email") ?? "";
  const inviteToken = searchParams.get("invite") ?? "";
  const [email, setEmail] = useState(presetEmail);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [localErr, setLocalErr] = useState<string | null>(null);
  // signupAllowed starts as null = "still probing"; once the public
  // config endpoint resolves, false means the deployment is
  // invite-only and we bounce back to /signin.
  const [signupAllowed, setSignupAllowed] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .getPublicAuthConfig()
      .then((r) => {
        if (!cancelled) setSignupAllowed(!!r.signup_enabled);
      })
      .catch(() => {
        if (!cancelled) setSignupAllowed(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (signupAllowed === null) return <div />;
  if (!signupAllowed) {
    // Invite-only deployment. Preserve any deep-link query params so a
    // sign-in followed by /invite/<token> still works.
    const search = searchParams.toString();
    return <Navigate to={`/signin${search ? `?${search}` : ""}`} replace />;
  }

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
            // Land on the invite-accept page if the signup came from
            // an invitation link; otherwise the usual welcome wizard.
            navigate(inviteToken ? `/invite/${inviteToken}` : "/welcome");
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
          aria-describedby="signup-password-hint"
        />
        {/* Surface the 8-character minimum BEFORE submit so the user
            doesn't pick a short password, round-trip the server, and
            then learn the rule from the error message. The hint sits
            under the field as low-emphasis copy. */}
        <div id="signup-password-hint" className="signup-hint">
          {t("signUp.passwordHint")}
        </div>
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
