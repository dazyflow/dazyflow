import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { orgFromHost } from "../lib/orgFromHost";

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
  const queryOrg = searchParams.get("org") ?? "";
  const [email, setEmail] = useState(presetEmail);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [googleEnabled, setGoogleEnabled] = useState(false);
  const [signupEnabled, setSignupEnabled] = useState(false);
  // The org whose SSO we offer. Prefer an explicit ?org=, otherwise fall
  // back to the org encoded in the host on a wildcard-subdomain deploy
  // (e.g. acme.hazyflow.app → "acme"), resolved once the public auth
  // config tells us the wildcard domain.
  const [org, setOrg] = useState(queryOrg);

  // Whether self-serve signup is enabled, plus the wildcard domain used
  // to derive the org from the host. Default signupEnabled false so the
  // "Create an account" link stays hidden until the probe confirms it's
  // allowed — matches the server's invite-only posture by default.
  useEffect(() => {
    let cancelled = false;
    api
      .getPublicAuthConfig()
      .then((r) => {
        if (cancelled) return;
        setSignupEnabled(!!r.signup_enabled);
        if (!queryOrg && r.wildcard_domain) {
          const fromHost = orgFromHost(
            window.location.hostname,
            r.wildcard_domain,
          );
          if (fromHost) setOrg(fromHost);
        }
      })
      .catch(() => {
        if (!cancelled) setSignupEnabled(false);
      });
    return () => {
      cancelled = true;
    };
  }, [queryOrg]);

  // Probe whether the resolved org has Google SSO turned on so we know
  // whether to render the button. Public lookup, no auth required.
  useEffect(() => {
    if (!org) {
      setGoogleEnabled(false);
      return;
    }
    let cancelled = false;
    api
      .getPublicSSOStatus(org)
      .then((r) => {
        if (!cancelled) setGoogleEnabled(!!r.google_enabled);
      })
      .catch(() => {
        if (!cancelled) setGoogleEnabled(false);
      });
    return () => {
      cancelled = true;
    };
  }, [org]);

  const googleHref = org
    ? `/api/v1/auth/google/start?tenant=${encodeURIComponent(org)}&return_to=${encodeURIComponent(
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
              <img src="/brands/google.svg" alt="" aria-hidden="true" />
              <span>{t("signIn.continueWithGoogle")}</span>
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
        {signupEnabled && (
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
        )}
      </form>
    </div>
  );
}
