import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";

// SignIn is the email+password sign-in form. It also handles two
// deep-link query params:
//   ?email=…   → pre-fills the email field (used by the invite-accept
//                landing when the user wasn't signed in yet)
//   ?invite=…  → after successful sign-in, navigate to /invite/<token>
//                so the invite-accept flow can pick up where it left off
//   ?org=…     → if that org has Google SSO configured, render a
//                "Sign in with Google" button alongside the password
//                form. The org is also the tenant the Google round-trip
//                lands the user in.
export function SignIn() {
  const { t } = useTranslation();
  const { signInWithPassword, error, loading } = useAuth();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const presetEmail = searchParams.get("email") ?? "";
  const inviteToken = searchParams.get("invite") ?? "";
  const orgID = searchParams.get("org") ?? "";
  const [email, setEmail] = useState(presetEmail);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [googleEnabled, setGoogleEnabled] = useState(false);

  // Probe whether the org has Google SSO turned on so we know whether
  // to render the button. Public lookup, no auth required.
  useEffect(() => {
    if (!orgID) {
      setGoogleEnabled(false);
      return;
    }
    let cancelled = false;
    api
      .getPublicSSOStatus(orgID)
      .then((r) => {
        if (!cancelled) setGoogleEnabled(!!r.google_enabled);
      })
      .catch(() => {
        if (!cancelled) setGoogleEnabled(false);
      });
    return () => {
      cancelled = true;
    };
  }, [orgID]);

  const googleHref = orgID
    ? `/api/v1/auth/google/start?tenant=${encodeURIComponent(orgID)}&return_to=${encodeURIComponent(
        inviteToken ? `/invite/${inviteToken}` : "/",
      )}`
    : "";

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
            if (inviteToken) {
              navigate(`/invite/${inviteToken}`);
            }
          } catch {
            /* error already set on context */
          } finally {
            setBusy(false);
          }
        }}
      >
        <h1>{t("signIn.title")}</h1>

        {googleEnabled && (
          <>
            <a href={googleHref} className="primary google-signin-btn">
              {t("signIn.continueWithGoogle")}
            </a>
            <div className="signin-divider">
              <span>{t("signIn.or")}</span>
            </div>
          </>
        )}

        <label htmlFor="email">{t("signIn.email")}</label>
        <input
          id="email"
          type="email"
          autoComplete="username"
          autoFocus={!presetEmail}
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
        <label htmlFor="password">{t("signIn.password")}</label>
        <input
          id="password"
          type="password"
          autoComplete="current-password"
          autoFocus={!!presetEmail}
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
          {t("signIn.newHere")}{" "}
          <Link
            to={
              inviteToken
                ? `/signup?email=${encodeURIComponent(email)}&invite=${encodeURIComponent(inviteToken)}`
                : "/signup"
            }
          >
            {t("signIn.createAccount")}
          </Link>
        </div>
      </form>
    </div>
  );
}
