// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api } from "../../api";
import { orgFromHost } from "../../lib/orgFromHost";
import { isImageIcon } from "../../lib/iconImage";
import { Button, ButtonLink } from "../../components/ui/Button";
import { AuthLayout } from "../../components/auth/AuthLayout";
import { PasswordField } from "../../components/ui/PasswordField";
import { OtpInput } from "../../components/ui/OtpInput";

// SignIn is the email+password sign-in form. It also handles four deep-link
// query params:
//   ?email=…   → pre-fills the email field (used by the invite-accept landing)
//   ?invite=…  → after sign-in, navigate to /invite/<token> so the accept flow
//                picks up where it left off
//   ?org=…     → if that org has Google SSO configured, render a "Sign in with
//                Google" button alongside the password form. The org is also
//                the tenant the Google round-trip lands the user in.
//   signInRequired → set by the signed-out catch-all, where the visitor asked
//                for something other than the sign-in page. Says why the form is
//                on screen without claiming the page is missing: a protected
//                page and a typo look identical, and only one is a dead link.
export function SignIn({ signInRequired = false }: { signInRequired?: boolean } = {}) {
  const { t } = useTranslation();
  const { signInWithPassword, verifyTOTP, error, loading, clearError } = useAuth();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const presetEmail = searchParams.get("email") ?? "";
  const inviteToken = searchParams.get("invite") ?? "";
  const queryOrg = searchParams.get("org") ?? "";
  // SSO second factor: the Google callback bounces a 2FA user here with a
  // challenge token (it can't return JSON like the password leg). When present
  // we open directly on the code step and, on success, land them at return_to.
  const ssoChallenge = searchParams.get("totp_challenge") ?? "";
  // return_to comes straight from the URL, so validate it before navigate():
  // the server's safeReturnPath only guards the links IT mints, not a link an
  // attacker hands the victim directly. Require a single-slash rooted path
  // (reject "//evil.com" and "/\evil.com", which browsers treat as absolute)
  // so a crafted /signin?return_to=//evil.com can't bounce a freshly
  // authenticated user off-origin. Mirrors daemon/google_signin.go safeReturnPath.
  const rawReturnTo = searchParams.get("return_to") ?? "";
  const ssoReturnTo =
    rawReturnTo.startsWith("/") &&
    !rawReturnTo.startsWith("//") &&
    !rawReturnTo.startsWith("/\\")
      ? rawReturnTo
      : "";
  // Where a completed sign-in lands. Both paths below (password and the TOTP
  // second factor) MUST call this: the authenticated route tree has no
  // /signin route, so a sign-in that navigates nowhere leaves the router
  // sitting on /signin and the authenticated catch-all renders "page not
  // found" at the exact moment the user succeeded. "/" is the neutral
  // default — RootRedirect decides /welcome vs /overview from there.
  // replace:true so Back doesn't return to the form and strand them again.
  const landAfterSignIn = () => {
    if (inviteToken) navigate(`/invite/${inviteToken}`, { replace: true });
    else if (ssoReturnTo) navigate(ssoReturnTo, { replace: true });
    else navigate("/", { replace: true });
  };
  const [email, setEmail] = useState(presetEmail);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [challenge, setChallenge] = useState<string | null>(ssoChallenge || null);
  const [totpCode, setTotpCode] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [googleEnabled, setGoogleEnabled] = useState(false);
  const [signupEnabled, setSignupEnabled] = useState<boolean | null>(null);
  // The org whose SSO we offer. Prefer an explicit ?org=, otherwise fall
  // back to the org encoded in the host on a wildcard-subdomain deploy
  // (e.g. acme.dazyflow.app → "acme"), resolved once the public auth
  // config tells us the wildcard domain.
  const [org, setOrg] = useState(queryOrg);
  const [orgBrand, setOrgBrand] = useState<{ name: string; icon?: string } | null>(
    null,
  );

  // Clear any error left over from the sign-up page (or a prior visit) so a
  // stale message doesn't greet a fresh arrival on the sign-in form.
  useEffect(() => {
    clearError();
  }, [clearError]);

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
          if (fromHost) {
            api
              .resolveSubdomain(fromHost)
              .then((res) => {
                if (cancelled) return;
                setOrg(res.tenant || fromHost);
                if (res.display_name) {
                  setOrgBrand({ name: res.display_name, icon: res.icon });
                }
              })
              .catch(() => {
                if (!cancelled) setOrg(fromHost);
              });
          }
        }
      })
      .catch(() => {
        if (!cancelled) setSignupEnabled(false);
      });
    return () => {
      cancelled = true;
    };
  }, [queryOrg]);

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

  if (challenge) {
    // Exchange the code (or recovery code) for a session. Shared by the
    // form submit and the OTP boxes' auto-submit on the sixth digit; the
    // latter passes the completed code so we don't race React state.
    const submitTotp = async (codeOverride?: string) => {
      const code = useRecovery ? "" : (codeOverride ?? totpCode).trim();
      const recovery = useRecovery ? recoveryCode.trim() : "";
      if (!code && !recovery) return;
      if (busy || loading) return;
      setBusy(true);
      try {
        await verifyTOTP(challenge, code, recovery);
        landAfterSignIn();
      } catch {
        /* error already set on context */
      } finally {
        setBusy(false);
      }
    };
    return (
      <AuthLayout>
        <form
          className="signin"
          onSubmit={(e) => {
            e.preventDefault();
            void submitTotp();
          }}
        >
          <h1>{t("signIn.totpTitle")}</h1>
          {!useRecovery ? (
            <>
              <label>{t("signIn.totpCodeLabel")}</label>
              <OtpInput
                value={totpCode}
                onChange={setTotpCode}
                onComplete={(v) => void submitTotp(v)}
                disabled={busy || loading}
                autoFocus
                ariaLabel={t("signIn.totpCodeLabel")}
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
          <Button
            type="submit"
            variant="primary"
            disabled={
              busy ||
              loading ||
              (useRecovery ? !recoveryCode.trim() : !totpCode.trim())
            }
          >
            {busy ? t("signIn.submitting") : t("signIn.verify")}
          </Button>
          {error && <div className="error">{error}</div>}
          <div className="signin-alt">
            <Button
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
            </Button>
          </div>
          {/* Back out of the second-factor step to the email/password form.
              Without this the only escape from a mistyped email (now stuck on
              the code prompt) was a full page reload. */}
          <div className="signin-alt">
            <Button
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
            </Button>
          </div>
        </form>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout>
      <form
        className="signin"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!email.trim() || !password) return;
          setBusy(true);
          try {
            const r = await signInWithPassword(email.trim(), password);
            if (r.totpRequired && r.challenge) {
              setChallenge(r.challenge);
              return;
            }
            landAfterSignIn();
          } catch {
            /* error already set on context */
          } finally {
            setBusy(false);
          }
        }}
      >
        {signInRequired && (
          <div className="signin-notice">{t("signIn.signInRequired")}</div>
        )}
        {orgBrand ? (
          <div className="signin-org">
            {isImageIcon(orgBrand.icon) && (
              <img
                src={orgBrand.icon}
                alt=""
                className="signin-org-icon"
                width={56}
                height={56}
                draggable={false}
              />
            )}
            <h1>{t("signIn.titleOrg", { org: orgBrand.name })}</h1>
          </div>
        ) : (
          <h1>{t("common.signIn")}</h1>
        )}

        {googleEnabled && (
          <>
            <ButtonLink variant="primary" className="google-signin-btn" href={googleHref}>
              <img src="/brands/google.svg" alt="" aria-hidden="true" />
              <span>{t("signIn.continueWithGoogle")}</span>
            </ButtonLink>
            <div className="signin-divider">
              <span>{t("signIn.or")}</span>
            </div>
          </>
        )}

        <label htmlFor="email">{t("common.email")}</label>
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
        <PasswordField
          id="password"
          autoComplete="current-password"
          autoFocus={!!presetEmail}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <Button
          type="submit"
          variant="primary"
          disabled={busy || loading || !email.trim() || !password}
        >
          {busy ? t("signIn.submitting") : t("common.signIn")}
        </Button>
        {error && <div className="error">{error}</div>}
        <div className="signin-alt">
          <Link
            to={email.trim() ? `/forgot-password?email=${encodeURIComponent(email.trim())}` : "/forgot-password"}
            onClick={() => clearError()}
          >
            {t("signIn.forgotPassword")}
          </Link>
        </div>
        {signupEnabled === true && (
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
        {/* Invite-only deployment: say so, rather than silently omitting the
            link — otherwise a user told to "sign up" sees no way to and
            assumes the page is broken. */}
        {signupEnabled === false && (
          <div className="signin-alt">{t("signIn.inviteOnly")}</div>
        )}
      </form>
    </AuthLayout>
  );
}
