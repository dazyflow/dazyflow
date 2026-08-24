// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { PasswordField } from "../../components/ui/PasswordField";
import { useAuth } from "../../auth";
import { api } from "../../api";

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
  const { signUpWithPassword, error, loading, clearError } = useAuth();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  // Pre-fill from /invite-flow deep links so the recipient doesn't
  // have to retype the email the invitation was sent to. The invite
  // token rides through to the post-signup redirect so they land
  // directly on the accept page.
  const presetEmail = searchParams.get("email") ?? "";
  const inviteToken = searchParams.get("invite") ?? "";
  // signupInvite is the platform-owner invite token (distinct from the
  // org-invite `invite` above). When present the email is locked to the
  // invited address, the page stays open even on a signup-disabled
  // deployment (the server validates the token), and there's no org to
  // accept into afterwards — the user lands on the normal welcome flow.
  const signupInvite = searchParams.get("signup_invite") ?? "";
  // Lock the email whenever we arrived via an invite link — both the
  // platform signup-invite (`signup_invite`, server-bound at signUp) and
  // the org-invite (`invite`, server-bound at the accept step in
  // orgs.go acceptInvitation, which 403s on a mismatched email). In both
  // cases the address is fixed to what the owner invited, so editing it
  // can only produce an account that can't accept — show it, don't allow
  // changing it.
  const lockEmail = !!signupInvite || !!inviteToken;
  const [email, setEmail] = useState(presetEmail);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [localErr, setLocalErr] = useState<string | null>(null);
  // signupAllowed starts as null = "still probing"; once the public
  // config endpoint resolves, false means the deployment is
  // invite-only and we bounce back to /signin. The page also stays
  // open when admin_bootstrap is set: signup is disabled deployment-
  // wide, but a platform-admin email can still claim its first account
  // here (the server enforces the allowlist — see httpsignup.go).
  const [signupAllowed, setSignupAllowed] = useState<boolean | null>(null);

  // Clear any error left over from the sign-in page so a stale "wrong
  // password" doesn't greet someone who just clicked through to sign up.
  useEffect(() => {
    clearError();
  }, [clearError]);

  useEffect(() => {
    // A signup-invite is its own authorization: the server checks the
    // token, so the page must stay open even when public signup is off.
    // Skip the probe entirely rather than risk bouncing the invitee.
    if (signupInvite) {
      setSignupAllowed(true);
      return;
    }
    let cancelled = false;
    api
      .getPublicAuthConfig()
      .then((r) => {
        if (!cancelled) setSignupAllowed(!!r.signup_enabled || !!r.admin_bootstrap);
      })
      .catch(() => {
        if (!cancelled) setSignupAllowed(false);
      });
    return () => {
      cancelled = true;
    };
  }, [signupInvite]);

  // Still probing whether signup is open. Show a quiet loading state in the
  // same frame as the form — a bare empty <div> read as a broken/blank page
  // on a slow connection.
  if (signupAllowed === null) {
    return (
      <div className="signin-wrap">
        <div className="signin" style={{ textAlign: "center", color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      </div>
    );
  }
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
            await signUpWithPassword(email.trim(), password, signupInvite);
            // A platform signup-invite creates the user's own account, so
            // there's no org to accept — straight to the welcome wizard.
            // An org-invite link (`invite`) still routes to its accept
            // page; everyone else gets the welcome wizard.
            navigate(
              inviteToken && !signupInvite
                ? `/invite/${inviteToken}`
                : "/welcome",
            );
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
          // On an invite, the email is fixed to the address the owner
          // invited — the server binds the new account to it, so let the
          // recipient see but not change it, and put focus on the
          // password they actually need to fill in.
          readOnly={lockEmail}
          autoFocus={!lockEmail}
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder="you@example.com"
        />
        <label htmlFor="password">{t("signUp.password")}</label>
        <PasswordField
          id="password"
          autoComplete="new-password"
          autoFocus={lockEmail}
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
        <PasswordField
          id="confirm"
          autoComplete="new-password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
        />
        <Button
          type="submit"
          variant="primary"
          disabled={
            busy || loading || !email.trim() || !password || passwordMismatch
          }
        >
          {busy ? t("signUp.submitting") : t("signUp.submit")}
        </Button>
        {(localErr || error) && <div className="error">{localErr ?? error}</div>}
        <div className="signin-alt">
          {t("signUp.haveAccount")} <Link to="/signin">{t("signUp.signInLink")}</Link>
        </div>
      </form>
    </div>
  );
}
