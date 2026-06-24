import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";
import { AlertCircle, Mail, ShieldCheck } from "lucide-react";
import { Button } from "../components/Button";
import { useAuth } from "../auth";
import { api } from "../api";
import type { InvitationDetails } from "../types";
import { formatDateTime } from "../lib/datetime";

// AcceptInvite is the landing page for an invite link. It's reachable
// without auth so a recipient can read who invited them and to which
// org before deciding to sign in. If they're already signed in (with
// the right email), the Accept button immediately binds the membership
// and routes to the new org. Otherwise we deep-link to /signin (or
// /signup) with the email pre-filled and the invite token preserved so
// the post-signin flow can continue from here.
export function AcceptInvite() {
  const { t } = useTranslation();
  const { token: inviteToken } = useParams();
  const navigate = useNavigate();
  const { token, me, setActiveTenant, signOut } = useAuth();
  const [details, setDetails] = useState<InvitationDetails | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [accepting, setAccepting] = useState(false);

  const load = useCallback(async () => {
    if (!inviteToken) return;
    try {
      const d = await api.viewInvitation(inviteToken);
      setDetails(d);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [inviteToken]);

  useEffect(() => {
    void load();
  }, [load]);

  const accept = async () => {
    if (!token || !inviteToken) return;
    setAccepting(true);
    setError(null);
    try {
      const r = await api.acceptInvitation(token, inviteToken);
      // Switch the session to the new org so the next page lands there.
      setActiveTenant(r.tenant);
      navigate("/");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setAccepting(false);
    }
  };

  if (!inviteToken) {
    return (
      <div className="signin-wrap">
        <div className="signin">
          <h1>{t("acceptInvite.missingToken")}</h1>
        </div>
      </div>
    );
  }
  if (error && !details) {
    return (
      <div className="signin-wrap">
        <div className="signin">
          <h1>{t("acceptInvite.title")}</h1>
          <div className="error">
            <AlertCircle size={14} style={{ verticalAlign: -2 }} /> {error}
          </div>
        </div>
      </div>
    );
  }
  if (!details) {
    return (
      <div className="signin-wrap">
        <div className="signin">
          <h1>{t("acceptInvite.title")}</h1>
          <div>{t("common.loading")}</div>
        </div>
      </div>
    );
  }

  const usable = details.pending;
  let statusLine: string | null = null;
  if (details.accepted) statusLine = t("acceptInvite.alreadyAccepted");
  else if (details.revoked) statusLine = t("acceptInvite.revoked");
  else if (details.expired) statusLine = t("acceptInvite.expired");

  const signedInAsOther =
    !!me?.subject && me.subject.toLowerCase() !== details.email.toLowerCase();
  const signedInAsRight = !!me?.subject && !signedInAsOther;

  return (
    <div className="signin-wrap">
      <div className="signin invite-card">
        <div className="invite-card-icon">
          <Mail size={28} />
        </div>
        <h1>{t("acceptInvite.title")}</h1>
        <p>
          <Trans
            i18nKey="acceptInvite.body"
            values={{
              email: details.email,
              org: details.tenant_display || details.tenant,
            }}
            components={[<strong />, <code />]}
          />
        </p>
        <ul className="invite-meta-list">
          <li>
            <ShieldCheck size={14} />
            {t("acceptInvite.rolesLine", {
              roles: details.roles.map((r) => r.name).join(", "),
            })}
          </li>
          <li>
            {t("acceptInvite.expiresLine", {
              when: formatDateTime(details.expires_at),
            })}
          </li>
          {details.invited_by && (
            <li>{t("acceptInvite.invitedByLine", { who: details.invited_by })}</li>
          )}
        </ul>

        {statusLine && (
          <div className="card" style={{ color: "var(--muted)" }}>
            {statusLine}
          </div>
        )}
        {error && (
          <div className="error">
            <AlertCircle size={14} style={{ verticalAlign: -2 }} /> {error}
          </div>
        )}

        {usable && signedInAsRight && (
          <Button variant="primary" onClick={accept} disabled={accepting}>
            {accepting ? t("acceptInvite.accepting") : t("acceptInvite.accept")}
          </Button>
        )}
        {usable && signedInAsOther && (
          <div className="card" style={{ color: "var(--danger)" }}>
            <Trans
              i18nKey="acceptInvite.wrongAccount"
              values={{ current: me?.subject, invited: details.email }}
              components={[<strong />, <strong />]}
            />
            {/* Escape hatch: sign out so the recipient can sign in as the
                invited address, instead of being stuck on this message. */}
            <div style={{ marginTop: "var(--space-3)" }}>
              <Button
                variant="primary"
                onClick={() => void signOut()}
              >
                {t("acceptInvite.signOutToSwitch")}
              </Button>
            </div>
          </div>
        )}
        {usable && !me && (
          <div className="invite-cta-row">
            <Link
              to={`/signin?email=${encodeURIComponent(details.email)}&invite=${encodeURIComponent(inviteToken)}`}
              className="primary signin-cta"
            >
              {t("acceptInvite.signInToAccept")}
            </Link>
            <Link
              to={`/signup?email=${encodeURIComponent(details.email)}&invite=${encodeURIComponent(inviteToken)}`}
            >
              {t("acceptInvite.signUpToAccept")}
            </Link>
          </div>
        )}
      </div>
    </div>
  );
}
