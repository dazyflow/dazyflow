import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Check, Copy, ShieldCheck } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import { ServiceIcon, serviceLabel } from "../components/ServiceIcon";
import type { OrgAuthConfig } from "../types";

// ssoUpcoming lists identity providers we show as placeholders so the
// surface reads as "SSO providers" rather than "Google" — the monogram
// tiles swap for real logos (and real config forms) as each lands.
// Empty for now: Microsoft Entra, Okta and SAML are hidden until wired.
const ssoUpcoming: string[] = [];

// RedirectURIDisplay shows the read-only redirect URI alongside a copy
// button. The URI must be pasted verbatim into Google's "Authorized
// redirect URIs" box — a trailing space or wrong scheme makes Google
// reject the sign-in, which is hard to debug. Manual copy is the
// single most error-prone step in the SSO setup, so we make it
// one-click.
function RedirectURIDisplay({ uri }: { uri: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(uri);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      /* clipboard may be blocked; user can select + copy manually */
    }
  };
  // No absolute origin to build a pasteable URI from — show a hint
  // instead of a misleading relative path in the copy box.
  if (!uri) {
    return <div className="desc">{t("admin.sso.redirectUriUnavailable")}</div>;
  }
  return (
    <div className="sso-readonly-row">
      <code className="sso-readonly">{uri}</code>
      <button type="button" onClick={copy} className="sso-copy-btn">
        {copied ? (
          <Check size={12} style={{ marginRight: 6, verticalAlign: -1 }} />
        ) : (
          <Copy size={12} style={{ marginRight: 6, verticalAlign: -1 }} />
        )}
        {copied ? t("admin.sso.copyRedirectDone") : t("admin.sso.copyRedirect")}
      </button>
    </div>
  );
}

// AdminOrgSSO is the per-organization Google Workspace SSO settings
// page. The org's admin pastes their Google OAuth client_id +
// client_secret here (the secret is write-only after first save:
// re-opening shows "secret stored" without revealing the value), plus
// an optional workspace domain (hd= claim) restriction.
//
// Once configured, members of this org can land on the sign-in page,
// pick the org from a small selector or hit a direct link, and bounce
// to Google. The auth/google_signin.go handlers on the daemon do the
// round-trip.
export function AdminOrgSSO() {
  const { t } = useTranslation();
  const { token, hasPerm, me } = useAuth();
  const [cfg, setCfg] = useState<OrgAuthConfig | null>(null);
  const [clientID, setClientID] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const [workspaceDomain, setWorkspaceDomain] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<Date | null>(null);
  const [testOK, setTestOK] = useState(false);
  const [testErrorCode, setTestErrorCode] = useState<string | null>(null);
  const [popupBlocked, setPopupBlocked] = useState(false);

  // The Test sign-in button opens /api/v1/auth/google/start in a new
  // tab. The callback either redirects back here with ?test=ok (full
  // round-trip succeeded — Google accepted client_id/secret and the
  // redirect URI matches) or ?test_error=<code> (the daemon classified
  // the failure). Read whichever is set, surface a banner, then strip
  // the param so a refresh doesn't keep claiming a stale result.
  useEffect(() => {
    if (typeof window === "undefined") return;
    const sp = new URLSearchParams(window.location.search);
    const ok = sp.get("test");
    const err = sp.get("test_error");
    let touched = false;
    if (ok === "ok") {
      setTestOK(true);
      sp.delete("test");
      touched = true;
    }
    if (err) {
      setTestErrorCode(err);
      sp.delete("test_error");
      touched = true;
    }
    if (touched) {
      const qs = sp.toString();
      const url = window.location.pathname + (qs ? `?${qs}` : "");
      window.history.replaceState(null, "", url);
    }
  }, []);

  // Codes the daemon emits — keep in sync with classifyGoogleError +
  // the redirectTestError sites in daemon/google_signin.go.
  const knownTestErrorCodes = new Set([
    "invalid_client",
    "redirect_uri_mismatch",
    "invalid_grant",
    "unauthorized_client",
    "exchange_failed",
    "no_email",
    "not_verified",
    "domain_mismatch",
    "not_configured",
    "denied",
    "internal",
  ]);
  const testErrorBodyKey =
    testErrorCode && knownTestErrorCodes.has(testErrorCode)
      ? `admin.sso.testError.${testErrorCode}`
      : "admin.sso.testError.exchange_failed";

  // silent skips the full-page loading swap — used by the post-save
  // refetch so the "Saved" chip stays visible instead of flashing away
  // under the loading card.
  const refresh = useCallback(
    async (silent = false) => {
      if (!token) return;
      if (!silent) setLoading(true);
      try {
        const c = await api.getOrgAuthConfig(token);
        setCfg(c);
        setClientID(c.google_client_id ?? "");
        setWorkspaceDomain(c.google_workspace_domain ?? "");
        setClientSecret(""); // never round-tripped from the server
        setError(null);
      } catch (e) {
        if (e instanceof APIError && e.status === 501) {
          setError(t("admin.sso.notConfigured"));
        } else {
          setError((e as Error).message);
        }
      } finally {
        if (!silent) setLoading(false);
      }
    },
    [token, t],
  );

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // The "Saved" confirmation is a transient acknowledgement, not durable
  // state — the header status pill carries whether SSO is actually on.
  // Fade it out after a few seconds so it doesn't linger as decoration.
  useEffect(() => {
    if (!savedAt) return;
    const id = window.setTimeout(() => setSavedAt(null), 4000);
    return () => window.clearTimeout(id);
  }, [savedAt]);

  if (!hasPerm("organization:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.sso.needAdmin" components={[<code />]} />
      </div>
    );
  }

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token) return;
    setSaving(true);
    setError(null);
    try {
      await api.putOrgAuthConfig(token, {
        google_client_id: clientID.trim(),
        // Empty secret = "keep existing" — the daemon honors this.
        google_client_secret: clientSecret || undefined,
        google_workspace_domain: workspaceDomain.trim() || undefined,
      });
      setSavedAt(new Date());
      setClientSecret("");
      void refresh(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  const disable = async () => {
    if (!token) return;
    if (!confirm(t("admin.sso.disableConfirm"))) return;
    try {
      await api.deleteOrgAuthConfig(token);
      setSavedAt(null);
      setCfg(null);
      setClientID("");
      setClientSecret("");
      setWorkspaceDomain("");
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const enabled = !!cfg?.google_enabled;
  const orgID = me?.tenant ?? "";
  const testSignInURL =
    `/api/v1/auth/google/start?tenant=${encodeURIComponent(orgID)}` +
    `&test=1` +
    `&return_to=${encodeURIComponent("/admin/sso")}`;
  // Public origin the daemon is reached at — prefer the operator-set
  // public_base_url, fall back to the current window origin.
  const publicOrigin = me?.public_base_url
    ? me.public_base_url.replace(/\/+$/, "")
    : typeof window !== "undefined"
      ? window.location.origin
      : "";
  // Without a known absolute origin the redirect URI would be a relative
  // path, which is invalid in Google's "Authorized redirect URIs" box and
  // would silently cause redirect_uri_mismatch. Leave it empty so the
  // display shows a "set the public base URL" hint rather than a
  // paste-ready-looking relative path.
  const redirectURI = publicOrigin ? `${publicOrigin}/api/v1/auth/google/callback` : "";
  const signinURL = `${publicOrigin}/signin?org=${encodeURIComponent(orgID)}`;

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <ShieldCheck size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.sso.title")}
          </h1>
          <div className="sub">{t("admin.sso.subtitle")}</div>
        </div>
      </div>

      <div className="svc-providers">
        <div className="svc-provider active">
          <ServiceIcon name="google" size={28} />
          <span className="svc-provider-name">{serviceLabel("google")}</span>
          {enabled && <span className="badge ok">{t("admin.sso.enabledBadge")}</span>}
        </div>
        {ssoUpcoming.map((s) => (
          <div className="svc-provider soon" key={s} aria-disabled="true">
            <ServiceIcon name={s} size={28} />
            <span className="svc-provider-name">{serviceLabel(s)}</span>
            <span className="badge muted">{t("admin.sso.soon")}</span>
          </div>
        ))}
      </div>

      {error && (
        <div
          className="card"
          role="alert"
          aria-live="assertive"
          style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}
        >
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}
      {testOK && (
        <div className="card sso-test-ok">
          <Check size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          <strong>{t("admin.sso.testSuccessHead")}</strong>
          <div className="desc">{t("admin.sso.testSuccessBody")}</div>
        </div>
      )}
      {testErrorCode && (
        <div className="card sso-test-error">
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          <strong>{t("admin.sso.testErrorHead")}</strong>
          <div className="desc">{t(testErrorBodyKey)}</div>
          <div className="desc sso-test-error-retry">{t("admin.sso.testErrorRetry")}</div>
        </div>
      )}
      {loading ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      ) : (
        <>
        <details className="sso-walkthrough card" open={!enabled}>
          <summary>{t("admin.sso.walkthroughSummary")}</summary>
          <p className="desc">{t("admin.sso.walkthroughIntro")}</p>
          <ol className="sso-walkthrough-steps">
            <li>
              <h3>{t("admin.sso.walkthroughStep1Head")}</h3>
              <p>
                <Trans
                  i18nKey="admin.sso.walkthroughStep1Body"
                  components={[
                    <a
                      href="https://console.cloud.google.com/apis/credentials"
                      target="_blank"
                      rel="noopener noreferrer"
                    />,
                  ]}
                />
              </p>
            </li>
            <li>
              <h3>{t("admin.sso.walkthroughStep2Head")}</h3>
              <p>
                <Trans
                  i18nKey="admin.sso.walkthroughStep2Body"
                  components={[<strong />]}
                />
              </p>
            </li>
            <li>
              <h3>{t("admin.sso.walkthroughStep3Head")}</h3>
              <p>
                <Trans
                  i18nKey="admin.sso.walkthroughStep3Body"
                  components={[<strong />, <strong />]}
                />
              </p>
              <RedirectURIDisplay uri={redirectURI} />
            </li>
            <li>
              <h3>{t("admin.sso.walkthroughStep4Head")}</h3>
              <p>
                <Trans
                  i18nKey="admin.sso.walkthroughStep4Body"
                  components={[<strong />, <strong />]}
                />
              </p>
            </li>
          </ol>
          <p className="desc sso-walkthrough-tip">{t("admin.sso.walkthroughTip")}</p>
        </details>
        <form className="card sso-card" onSubmit={save}>
          <div className="sso-card-head">
            <ServiceIcon name="google" size={36} className="sso-card-logo" />
            <div className="sso-card-headings">
              <h2>{t("admin.sso.googleHead")}</h2>
              <p className="desc">{t("admin.sso.googleIntro")}</p>
            </div>
            <span className={`sso-status-pill${enabled ? " is-active" : ""}`}>
              {enabled && <Check size={12} style={{ verticalAlign: -1 }} />}
              {enabled ? t("admin.sso.enabledBadge") : t("admin.sso.statusInactive")}
            </span>
          </div>

          <div className="sf-field">
            <label>{t("admin.sso.redirectUriLabel")}</label>
            <RedirectURIDisplay uri={redirectURI} />
            <div className="desc">{t("admin.sso.redirectUriDesc")}</div>
          </div>

          <div className="sf-field">
            <label>{t("admin.sso.clientIdLabel")}</label>
            <input
              type="text"
              value={clientID}
              onChange={(e) => setClientID(e.target.value)}
              placeholder="123456789-abcdef.apps.googleusercontent.com"
            />
            <div className="desc">{t("admin.sso.clientIdDesc")}</div>
          </div>

          <div className="sf-field">
            <label>{t("admin.sso.clientSecretLabel")}</label>
            <input
              type="password"
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              placeholder={cfg?.google_secret_set ? t("common.secretStored") : ""}
              autoComplete="off"
            />
            <div className="desc">
              {cfg?.google_secret_set
                ? t("admin.sso.clientSecretStored")
                : t("admin.sso.clientSecretDesc")}
            </div>
          </div>

          <div className="sf-field">
            <label>{t("admin.sso.workspaceDomainLabel")}</label>
            <input
              type="text"
              value={workspaceDomain}
              onChange={(e) => setWorkspaceDomain(e.target.value)}
              placeholder="acme.com"
            />
            <div className="desc">{t("admin.sso.workspaceDomainDesc")}</div>
          </div>

          <div className="sso-card-foot">
            <div className="sso-foot-msg" role="status" aria-live="polite">
              {savedAt && (
                <span className="sso-saved-chip">
                  <Check size={12} style={{ verticalAlign: -1 }} />
                  {t("admin.sso.savedAt")}
                </span>
              )}
            </div>
            <div className="sso-foot-actions">
              {enabled && (
                <button type="button" onClick={disable}>
                  {t("admin.sso.disable")}
                </button>
              )}
              <button type="submit" className="primary" disabled={saving}>
                {saving ? t("admin.sso.saving") : t("admin.sso.save")}
              </button>
            </div>
          </div>

          {enabled && (
            <div className="sso-active-row">
              <strong>{t("admin.sso.activeHead")}</strong>
              <div className="desc">
                <Trans
                  i18nKey="admin.sso.activeBody"
                  values={{ signinUrl: signinURL }}
                  components={[<code />]}
                />
              </div>
              <div className="sso-test-row">
                <button
                  type="button"
                  className="primary"
                  onClick={() => {
                    // window.open returns null when the browser blocks
                    // the popup (common with strict popup-blocker
                    // settings). Fall back to an inline link the user
                    // can click in-tab — losing the dual-tab UX but
                    // not losing the test flow entirely.
                    const w = window.open(testSignInURL, "_blank", "noopener,noreferrer");
                    setPopupBlocked(!w);
                  }}
                >
                  {t("admin.sso.testButton")}
                </button>
                <div className="desc">{t("admin.sso.testButtonDesc")}</div>
                {popupBlocked && (
                  <div className="sso-popup-blocked">
                    <AlertCircle size={12} style={{ marginRight: 6, verticalAlign: -1 }} />
                    <strong>{t("admin.sso.popupBlockedHead")}</strong>
                    <div className="desc">
                      {t("admin.sso.popupBlockedBody")}{" "}
                      <a href={testSignInURL}>{t("admin.sso.popupBlockedLink")}</a>{" "}
                      {t("admin.sso.popupBlockedTail")}
                    </div>
                  </div>
                )}
              </div>
            </div>
          )}
        </form>
        </>
      )}
    </div>
  );
}
