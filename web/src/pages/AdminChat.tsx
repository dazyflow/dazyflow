import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Check, Sparkles } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";

// ANTHROPIC_KEY_NAME is the well-known secret name the daemon's chat
// agent reads on every request, keyed by the caller's tenant. Must
// match daemon.TenantAnthropicKeyName on the server.
const ANTHROPIC_KEY_NAME = "anthropic_api_key";

// AdminChat is the per-tenant Anthropic API key surface. The chat
// agent uses BYO-key, so each org sets their own key here. Stored in
// the encrypted secret store under "anthropic_api_key", scoped to the
// caller's tenant. The daemon never has a fallback key, by design.
export function AdminChat() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [keySet, setKeySet] = useState<boolean | null>(null);
  const [keyInput, setKeyInput] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<Date | null>(null);
  const [storeOff, setStoreOff] = useState(false);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listSecrets(token);
      setKeySet(r.secrets.includes(ANTHROPIC_KEY_NAME));
      setStoreOff(false);
      setError(null);
    } catch (e) {
      if (e instanceof APIError && e.status === 501) {
        setStoreOff(true);
      } else {
        setError((e as Error).message);
      }
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("tenant:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.chat.needAdmin" components={[<code />]} />
      </div>
    );
  }

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token) return;
    const value = keyInput.trim();
    if (!value) {
      setError(t("admin.chat.keyRequired"));
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await api.putSecret(token, ANTHROPIC_KEY_NAME, value);
      setSavedAt(new Date());
      setKeyInput("");
      void refresh();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const clear = async () => {
    if (!token) return;
    if (!confirm(t("admin.chat.clearConfirm"))) return;
    try {
      await api.deleteSecret(token, ANTHROPIC_KEY_NAME);
      setSavedAt(null);
      void refresh();
    } catch (err) {
      setError((err as Error).message);
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Sparkles size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.chat.title")}
          </h1>
          <div className="sub">{t("admin.chat.subtitle")}</div>
        </div>
      </div>

      {storeOff && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {t("admin.chat.storeOff")}
        </div>
      )}

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {!storeOff && (
        <form className="card" onSubmit={save}>
          <h3 style={{ marginTop: 0 }}>{t("admin.chat.keyHead")}</h3>
          <p className="desc" style={{ marginTop: 0 }}>
            <Trans i18nKey="admin.chat.keyIntro" components={[<a href="https://console.anthropic.com/settings/keys" target="_blank" rel="noopener noreferrer" />]} />
          </p>

          <div className="sf-field">
            <label htmlFor="anthropic-key">{t("admin.chat.keyLabel")}</label>
            <input
              id="anthropic-key"
              type="password"
              autoComplete="off"
              spellCheck={false}
              value={keyInput}
              onChange={(e) => setKeyInput(e.target.value)}
              placeholder={
                keySet
                  ? t("admin.chat.keyPlaceholderStored")
                  : t("admin.chat.keyPlaceholderEmpty")
              }
              disabled={loading || saving}
            />
            <div className="desc">
              {keySet
                ? t("admin.chat.keyStoredDesc")
                : t("admin.chat.keyEmptyDesc")}
            </div>
          </div>

          <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
            <button
              type="submit"
              className="primary"
              disabled={saving || !keyInput.trim()}
            >
              {saving ? t("admin.chat.saving") : t("admin.chat.save")}
            </button>
            {keySet && (
              <button
                type="button"
                className="ghost"
                onClick={clear}
                disabled={saving}
              >
                {t("admin.chat.clear")}
              </button>
            )}
            {savedAt && (
              <span style={{ color: "var(--success)", fontSize: 12 }}>
                <Check size={12} style={{ verticalAlign: -2 }} /> {t("admin.chat.saved")}
              </span>
            )}
          </div>
        </form>
      )}
    </div>
  );
}
