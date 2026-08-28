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
//   notFound   → rendered by the signed-out catch-all route, so a mistyped or
//                expired link says so instead of looking like a plain sign-in
//                prompt the visitor was asked for out of nowhere.
export function SignIn({ notFound = false }: { notFound?: boolean } = {}) {
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
  const [email, setEmail] = useState(presetEmail);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  // Second-factor step. When the password step returns a challenge we
  // swap the form to a code prompt rather than navigating — the
  // challenge is short-lived and single-use, so it lives only in state.
  const [challenge, setChallenge] = useState<string | null>(ssoChallenge || null);
  const [totpCode, setTotpCode] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [googleEnabled, setGoogleEnabled] = useState(false);
  // null = still probing; true/false = the deployment's answer. Tri-state so
  // we can show an explicit "invitation only" note when signup is off without
  // flashing it during the probe on a signup-enabled deployment.
  const [signupEnabled, setSignupEnabled] = useState<boolean | null>(null);
  // The org whose SSO we offer. Prefer an explicit ?org=, otherwise fall
  // back to the org encoded in the host on a wildcard-subdomain deploy
  // (e.g. acme.dazyflow.app → "acme"), resolved once the public auth
  // config tells us the wildcard domain.
  const [org, setOrg] = useState(queryOrg);
  // When the org is resolved from the host subdomain, its display name + icon
  // brand the sign-in page ("Sign in to Klahr" with the org logo), so a member
  // arriving at klahr.dazyflow.app sees they're in the right place.
  const [orgBrand, setOrgBrand] = useState<{ name: string; icon?: string } | null>(
    null,
  );

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
          if (fromHost) {
            // The host label is a user-chosen alias, not the tenant ID — resolve
            // it to the real tenant so the SSO probe + Google start target the
            // right org. If the label isn't claimed (or resolution fails), fall
            // back to using it verbatim — back-compat for deploys where the
            // tenant ID itself is the label.
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
        if (inviteToken) {
          navigate(`/invite/${inviteToken}`);
        } else if (ssoReturnTo) {
          navigate(ssoReturnTo);
        }
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
        {notFound && (
          <div className="signin-notice">{t("signIn.notFound")}</div>
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
