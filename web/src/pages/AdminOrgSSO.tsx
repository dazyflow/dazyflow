import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Check, ShieldCheck } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { OrgAuthConfig } from "../types";

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

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
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
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("tenant:admin")) {
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
      void refresh();
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
  const redirectURI =
    me?.public_base_url
      ? `${me.public_base_url.replace(/\/+$/, "")}/api/v1/auth/google/callback`
      : (typeof window !== "undefined"
          ? `${window.location.origin}/api/v1/auth/google/callback`
          : "/api/v1/auth/google/callback");

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

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}
      {loading ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      ) : (
        <form className="card" onSubmit={save}>
          <h2 style={{ marginTop: 0 }}>{t("admin.sso.googleHead")}</h2>
          <p className="desc">{t("admin.sso.googleIntro")}</p>

          <div className="sf-field">
            <label>{t("admin.sso.redirectUriLabel")}</label>
            <code className="sso-readonly">{redirectURI}</code>
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
              placeholder={cfg?.google_secret_set ? t("admin.sso.secretStoredPlaceholder") : ""}
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

          <div className="settings-foot" style={{ borderTop: "none", padding: 0 }}>
            {enabled && (
              <button type="button" onClick={disable}>
                {t("admin.sso.disable")}
              </button>
            )}
            <button type="submit" className="primary" disabled={saving}>
              {saving ? t("admin.sso.saving") : t("admin.sso.save")}
            </button>
            {savedAt && (
              <span className="desc" style={{ marginLeft: 12, color: "var(--success)" }}>
                <Check size={12} style={{ verticalAlign: -1 }} />
                {t("admin.sso.savedAt")}
              </span>
            )}
          </div>

          {enabled && (
            <div className="sso-active-row">
              <strong>{t("admin.sso.activeHead")}</strong>
              <div className="desc">
                {t("admin.sso.activeBody", {
                  signinUrl: `/signin?org=${encodeURIComponent(orgID)}`,
                })}
              </div>
            </div>
          )}
        </form>
      )}
    </div>
  );
}
