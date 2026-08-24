// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Save, Trash2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import { Button } from "../components/Button";
import { ServiceIcon } from "../components/ServiceIcon";
import type { AdminOAuthProvider } from "../types";
import { explainApiError } from "../lib/explainApiError";
import { ErrorNotice } from "../components/ErrorNotice";
import { ICON } from "../icons";

// AdminOAuthProviders is the paste-client-credentials surface that
// replaces "edit env vars + restart the daemon" for an operator setting
// up Google / Slack / GitHub / Notion. Each known provider shows up as a
// row whether or not it's configured; saving persists encrypted-at-rest
// and live-registers the provider so the next /oauth/authorize works
// without a restart.
export function AdminOAuthProviders() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [providers, setProviders] = useState<AdminOAuthProvider[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listAdminOAuthProviders(token);
      setProviders(r.providers ?? []);
      setError(null);
    } catch (e) {
      const err = e as APIError | Error;
      if (err instanceof APIError && err.status === 501) {
        setError(t("admin.oauth.notConfigured"));
      } else {
        setError(explainApiError(err, t));
      }
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("organization:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.oauth.needAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("admin.oauth.title")}</h1>
          <div className="sub">{t("admin.oauth.subtitle")}</div>
        </div>
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-4)" }}>
          {error}
        </ErrorNotice>
      )}

      {loading && !providers.length ? (
        <div className="card">{t("common.loading")}</div>
      ) : (
        <div className="svc-list">
          {providers.map((p) => (
            <ProviderRow key={p.name} provider={p} onChanged={refresh} />
          ))}
        </div>
      )}
    </div>
  );
}

function ProviderRow({
  provider,
  onChanged,
}: {
  provider: AdminOAuthProvider;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  // The client ID is a public identifier, so we pre-fill it with the
  // configured value (and keep it in sync when the list refetches after a
  // save/clear). The client_secret is never read back — paste-only — so
  // its field stays a local draft, blank until the operator types.
  const [clientID, setClientID] = useState(provider.client_id ?? "");
  const [clientSecret, setClientSecret] = useState("");
  useEffect(() => {
    setClientID(provider.client_id ?? "");
  }, [provider.client_id]);
  const [saving, setSaving] = useState(false);
  const [clearing, setClearing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);

  const save = async () => {
    if (!token) return;
    if (!clientID.trim() || !clientSecret.trim()) {
      setError(t("admin.oauth.bothRequired"));
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await api.upsertAdminOAuthProvider(token, provider.name, clientID.trim(), clientSecret.trim());
      // Leave the client ID in place (the refetch re-syncs it); only the
      // write-only secret draft is cleared.
      setClientSecret("");
      onChanged();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  };

  const clear = async () => {
    if (!token) return;
    setClearing(true);
    setError(null);
    try {
      await api.deleteAdminOAuthProvider(token, provider.name);
      setConfirmClear(false);
      onChanged();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setClearing(false);
    }
  };

  return (
    <div className="card svc-card">
      <div className="svc-head">
        <ServiceIcon name={provider.name} size={32} />
        <div className="svc-head-text">
          <h3>{provider.display_name}</h3>
          <span className="svc-redirect" title={provider.redirect_uri}>
            {provider.redirect_uri}
          </span>
        </div>
        {provider.configured ? (
          <span className="badge ok">
            {provider.has_env ? t("admin.oauth.fromEnv") : t("admin.oauth.configured")}
          </span>
        ) : (
          <span className="badge muted">{t("admin.oauth.notConfiguredBadge")}</span>
        )}
      </div>

      <div className="svc-fields">
        <label>
          <span>{t("admin.oauth.clientIDLabel")}</span>
          <input
            type="text"
            value={clientID}
            placeholder={provider.configured ? t("admin.oauth.clientIDPasteToOverride") : ""}
            onChange={(e) => setClientID(e.target.value)}
            style={{ fontFamily: "var(--font-mono)" }}
          />
        </label>
        <label>
          <span>{t("admin.oauth.clientSecretLabel")}</span>
          <input
            type="password"
            value={clientSecret}
            placeholder={provider.configured ? t("common.secretStored") : ""}
            onChange={(e) => setClientSecret(e.target.value)}
            style={{ fontFamily: "var(--font-mono)" }}
          />
        </label>
      </div>

      {error && <div className="svc-error">{error}</div>}

      <div className="svc-actions">
        <Button variant="primary" onClick={save} disabled={saving || !clientID.trim() || !clientSecret.trim()}>
          <Save size={ICON.sm} style={{ marginRight: 4 }} />
          {saving ? t("common.saving") : t("common.save")}
        </Button>
        {provider.has_persisted && !confirmClear && (
          <Button variant="ghost" onClick={() => setConfirmClear(true)}>
            <Trash2 size={ICON.sm} style={{ marginRight: 4 }} />
            {t("admin.oauth.clear")}
          </Button>
        )}
        {confirmClear && (
          <>
            <span className="svc-confirm">{t("admin.oauth.clearConfirm")}</span>
            <Button variant="ghost" className="danger" onClick={clear} disabled={clearing}>
              {clearing ? t("admin.oauth.clearing") : t("admin.oauth.clearYes")}
            </Button>
            <Button variant="ghost" onClick={() => setConfirmClear(false)}>
              {t("common.cancel")}
            </Button>
          </>
        )}
      </div>
    </div>
  );
}
