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
  const { signInWithPassword, verifyTOTP, error, loading, clearError } = useAuth();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const presetEmail = searchParams.get("email") ?? "";
  const inviteToken = searchParams.get("invite") ?? "";
  const queryOrg = searchParams.get("org") ?? "";
  const [email, setEmail] = useState(presetEmail);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  // Second-factor step. When the password step returns a challenge we
  // swap the form to a code prompt rather than navigating — the
  // challenge is short-lived and single-use, so it lives only in state.
  const [challenge, setChallenge] = useState<string | null>(null);
  const [totpCode, setTotpCode] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [googleEnabled, setGoogleEnabled] = useState(false);
  const [signupEnabled, setSignupEnabled] = useState(false);
  // The org whose SSO we offer. Prefer an explicit ?org=, otherwise fall
  // back to the org encoded in the host on a wildcard-subdomain deploy
  // (e.g. acme.hazyflow.app → "acme"), resolved once the public auth
  // config tells us the wildcard domain.
  const [org, setOrg] = useState(queryOrg);

  // Clear any error left over from the sign-up page (or a prior visit) so a
  // stale message doesn't greet a fresh arrival on the sign-in form.
  useEffect(() => {
    clearError();
  }, [clearError]);

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

  // Second-factor step: shown once the password step hands back a
  // challenge. Submitting exchanges the code (or recovery code) for a
  // session via verifyTOTP, then follows the same post-sign-in nav as
  // the password path.
  if (challenge) {
    return (
      <div className="signin-wrap">
        <form
          className="signin"
          onSubmit={async (e) => {
            e.preventDefault();
            const code = useRecovery ? "" : totpCode.trim();
            const recovery = useRecovery ? recoveryCode.trim() : "";
            if (!code && !recovery) return;
            setBusy(true);
            try {
              await verifyTOTP(challenge, code, recovery);
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
          <h1>{t("signIn.totpTitle")}</h1>
          {!useRecovery ? (
            <>
              <label htmlFor="totp-code">{t("signIn.totpCodeLabel")}</label>
              <input
                id="totp-code"
                type="text"
                inputMode="numeric"
                autoComplete="one-time-code"
                autoFocus
                value={totpCode}
                onChange={(e) => setTotpCode(e.target.value)}
                placeholder="123456"
              />
              <div className="desc">{t("signIn.totpCodeHint")}</div>
            </>
          ) : (
            <>
              <label htmlFor="recovery-code">
                {t("signIn.recoveryCodeLabel")}
              </label>
              <input
                id="recovery-code"
                type="text"
                autoComplete="one-time-code"
                autoFocus
                value={recoveryCode}
                onChange={(e) => setRecoveryCode(e.target.value)}
                placeholder="xxxx-xxxx"
              />
              <div className="desc">{t("signIn.recoveryCodeHint")}</div>
            </>
          )}
          <button
            type="submit"
            className="primary"
            disabled={
              busy ||
              loading ||
              (useRecovery ? !recoveryCode.trim() : !totpCode.trim())
            }
          >
            {busy ? t("signIn.submitting") : t("signIn.verify")}
          </button>
          {error && <div className="error">{error}</div>}
          <div className="signin-alt">
            <button
              type="button"
              className="linklike"
              onClick={() => {
                setUseRecovery((v) => !v);
                setTotpCode("");
                setRecoveryCode("");
              }}
            >
              {useRecovery
                ? t("signIn.useAuthenticator")
                : t("signIn.useRecoveryCode")}
            </button>
          </div>
          {/* Back out of the second-factor step to the email/password form.
              Without this the only escape from a mistyped email (now stuck on
              the code prompt) was a full page reload. */}
          <div className="signin-alt">
            <button
              type="button"
              className="linklike"
              onClick={() => {
                setChallenge(null);
                setTotpCode("");
                setRecoveryCode("");
                setUseRecovery(false);
                clearError();
              }}
            >
              {t("signIn.back")}
            </button>
          </div>
        </form>
      </div>
    );
  }

  return (
    <div className="signin-wrap">
      <form
        className="signin"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!email.trim() || !password) return;
          setBusy(true);
          try {
            const r = await signInWithPassword(email.trim(), password);
            // 2FA: don't navigate — switch the form to the code step.
            if (r.totpRequired && r.challenge) {
              setChallenge(r.challenge);
              return;
            }
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
              onClick={() => clearError()}
            >
              {t("signIn.createAccount")}
            </Link>
          </div>
        )}
      </form>
    </div>
  );
}
