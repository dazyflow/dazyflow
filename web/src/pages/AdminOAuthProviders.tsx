import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Plug, Save, Trash2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { AdminOAuthProvider } from "../types";

// AdminOAuthProviders is the paste-client-credentials surface that
// replaces "edit env vars + restart the daemon" for an operator
// setting up Google / Slack / GitHub / Notion. Each known provider
// shows up as a row whether or not it's configured; saving here
// persists encrypted-at-rest and live-registers the provider so the
// next /oauth/authorize works without a restart.
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
        setError(err.message);
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
        <Trans i18nKey="admin.oauth.needAdmin" components={[<code />]} />
      </div>
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
        <div className="card error" style={{ marginBottom: 12 }}>
          <AlertCircle size={14} style={{ verticalAlign: -2, marginRight: 6 }} />
          {error}
        </div>
      )}

      {loading && !providers.length ? (
        <div className="card">{t("common.loading")}</div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
          {providers.map((p) => (
            <ProviderRow
              key={p.name}
              provider={p}
              onSaved={refresh}
              onCleared={refresh}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function ProviderRow({
  provider,
  onSaved,
  onCleared,
}: {
  provider: AdminOAuthProvider;
  onSaved: () => void;
  onCleared: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  // The persisted client_secret is never read back — paste-only — so
  // each row keeps a local draft. clientID is shown when empty as a
  // gentle "you haven't pasted anything yet" cue; we don't pre-fill
  // it from any cached state.
  const [clientID, setClientID] = useState("");
  const [clientSecret, setClientSecret] = useState("");
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
      await api.upsertAdminOAuthProvider(
        token,
        provider.name,
        clientID.trim(),
        clientSecret.trim(),
      );
      setClientID("");
      setClientSecret("");
      onSaved();
    } catch (e) {
      setError((e as Error).message);
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
      onCleared();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setClearing(false);
    }
  };

  return (
    <div className="card">
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: 12,
          marginBottom: 8,
        }}
      >
        <h3 style={{ margin: 0, display: "flex", alignItems: "center", gap: 8 }}>
          <Plug size={16} />
          {provider.display_name}
        </h3>
        <div style={{ display: "flex", gap: 6 }}>
          {provider.configured && (
            <span className="badge">
              {provider.has_env
                ? t("admin.oauth.fromEnv")
                : t("admin.oauth.configured")}
            </span>
          )}
          {!provider.configured && (
            <span className="badge" style={{ background: "var(--warn-soft, #f6e6c8)" }}>
              {t("admin.oauth.notConfiguredBadge")}
            </span>
          )}
        </div>
      </div>

      <p style={{ color: "var(--muted)", fontSize: 13, margin: "0 0 8px 0" }}>
        {provider.setup_help}
      </p>

      <div
        style={{
          background: "var(--surface-2, #f7f8fa)",
          padding: "8px 10px",
          borderRadius: "var(--r-1, 4px)",
          fontSize: 12,
          marginBottom: 10,
        }}
      >
        <div>
          <strong>{t("admin.oauth.redirectURILabel")}: </strong>
          <code style={{ fontFamily: "var(--font-mono)" }}>
            {provider.redirect_uri}
          </code>
        </div>
        {provider.scopes.length > 0 && (
          <div style={{ marginTop: 4 }}>
            <strong>{t("admin.oauth.scopesLabel")}: </strong>
            <code style={{ fontFamily: "var(--font-mono)", fontSize: 11 }}>
              {provider.scopes.join(" ")}
            </code>
          </div>
        )}
        {provider.updated_at && (
          <div style={{ marginTop: 4 }}>
            <strong>{t("admin.oauth.updatedAtLabel")}: </strong>
            {new Date(provider.updated_at).toLocaleString()}
          </div>
        )}
      </div>

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "1fr 1fr",
          gap: 8,
          marginBottom: 8,
        }}
      >
        <label style={{ fontSize: 12 }}>
          <div>{t("admin.oauth.clientIDLabel")}</div>
          <input
            type="text"
            value={clientID}
            placeholder={
              provider.configured
                ? t("admin.oauth.clientIDPasteToOverride")
                : ""
            }
            onChange={(e) => setClientID(e.target.value)}
            style={{ width: "100%", fontFamily: "var(--font-mono)" }}
          />
        </label>
        <label style={{ fontSize: 12 }}>
          <div>{t("admin.oauth.clientSecretLabel")}</div>
          <input
            type="password"
            value={clientSecret}
            placeholder={
              provider.configured
                ? t("admin.oauth.clientSecretPasteToOverride")
                : ""
            }
            onChange={(e) => setClientSecret(e.target.value)}
            style={{ width: "100%", fontFamily: "var(--font-mono)" }}
          />
        </label>
      </div>

      {error && (
        <div
          style={{
            color: "var(--danger)",
            fontSize: 12,
            marginBottom: 6,
          }}
        >
          {error}
        </div>
      )}

      <div style={{ display: "flex", gap: 8 }}>
        <button
          className="primary"
          onClick={save}
          disabled={saving || !clientID.trim() || !clientSecret.trim()}
        >
          <Save size={14} style={{ verticalAlign: -2, marginRight: 4 }} />
          {saving ? t("admin.oauth.saving") : t("admin.oauth.save")}
        </button>
        {provider.has_persisted && !confirmClear && (
          <button className="ghost" onClick={() => setConfirmClear(true)}>
            <Trash2 size={14} style={{ verticalAlign: -2, marginRight: 4 }} />
            {t("admin.oauth.clear")}
          </button>
        )}
        {confirmClear && (
          <>
            <span style={{ fontSize: 12, alignSelf: "center" }}>
              {t("admin.oauth.clearConfirm")}
            </span>
            <button
              className="ghost"
              onClick={clear}
              disabled={clearing}
              style={{ color: "var(--danger)" }}
            >
              {clearing ? t("admin.oauth.clearing") : t("admin.oauth.clearYes")}
            </button>
            <button className="ghost" onClick={() => setConfirmClear(false)}>
              {t("admin.oauth.clearNo")}
            </button>
          </>
        )}
      </div>
    </div>
  );
}
