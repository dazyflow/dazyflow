import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Box, Check, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { oauthProviderDisplay } from "../integrationMeta";
import type { OAuthProviderStatus } from "../types";

// Connections is the actionable "connect an app once, then your flows
// can use it" page. Two halves:
//   - OAuth providers (Connect button per provider; connected
//     accounts shown as chips). Source: GET /oauth/providers.
//   - Credentials: tenant secrets the user types in by hand (a
//     database URL, an API token). Source: GET /secrets, filtered to
//     hide the oauth.<provider>.<account> entries the Connect flow
//     manages on its own.
// Either half hides itself when the daemon reports the feature isn't
// configured (501) or the caller can't use it (401/403) so a minimal
// install or a low-privilege user doesn't see dead controls.

// RETURN_TO is where the OAuth round-trip bounces the browser back to.
// Must be a same-origin path the daemon will accept.
const RETURN_TO = "/connections";

// featureUnavailable: statuses that mean "this connections feature
// isn't usable for this caller" — not configured (501) or not
// permitted (401/403). All map to "hide the section" rather than an
// error banner.
function featureUnavailable(status: number): boolean {
  return status === 501 || status === 401 || status === 403;
}

export function Connections() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();

  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(null);
  const [providersOff, setProvidersOff] = useState(false);
  const [secrets, setSecrets] = useState<string[] | null>(null);
  const [secretsOff, setSecretsOff] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // OAuth callback banner: the daemon bounces the browser back here
  // with ?oauth=success|error&provider=&account=[&error=]. Read it
  // once, then strip it from the URL so a refresh doesn't re-show it.
  const oauthResult = searchParams.get("oauth");
  const oauthProvider = searchParams.get("provider") ?? "";
  const oauthAccount = searchParams.get("account") ?? "";
  const oauthError = searchParams.get("error") ?? "";

  const refresh = () => {
    if (!token) return;
    api
      .listProviders(token)
      .then((r) => {
        setProviders(r.providers);
        setProvidersOff(false);
      })
      .catch((e) => {
        if (e instanceof APIError && featureUnavailable(e.status))
          setProvidersOff(true);
        else setError(e instanceof APIError ? e.message : (e as Error).message);
      });
    api
      .listSecrets(token)
      .then((r) => {
        setSecrets(r.secrets);
        setSecretsOff(false);
      })
      .catch((e) => {
        if (e instanceof APIError && featureUnavailable(e.status))
          setSecretsOff(true);
        else setError(e instanceof APIError ? e.message : (e as Error).message);
      });
  };

  useEffect(refresh, [token]);

  const dismissBanner = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("oauth");
    next.delete("provider");
    next.delete("account");
    next.delete("error");
    setSearchParams(next, { replace: true });
  };

  const connect = (provider: string) => {
    if (!token) return;
    // Full-page navigation: the daemon 302s to the provider's consent
    // screen. Auth rides on the session cookie.
    window.location.assign(api.oauthAuthorizeUrl(provider, RETURN_TO));
  };

  // Hide the oauth.* secrets from the credentials list — those are
  // surfaced as connected-account chips under their provider.
  const userSecrets = useMemo(
    () => (secrets ?? []).filter((n) => !n.startsWith("oauth.")),
    [secrets],
  );

  return (
    <div className="page connections-page">
      <h1>{t("connections.title")}</h1>
      <p className="page-sub">{t("connections.intro")}</p>

      {providersOff && secretsOff && (
        <div className="card connections-disabled">
          {t("connections.allDisabled")}
        </div>
      )}

      {oauthResult === "success" && (
        <div className="card connections-banner success">
          <span>
            {t("connections.connectedOk", {
              provider: oauthProviderDisplay(oauthProvider).name,
              account: oauthAccount,
            })}
          </span>
          <button type="button" className="link-button" onClick={dismissBanner}>
            {t("common.dismiss")}
          </button>
        </div>
      )}
      {oauthResult === "error" && (
        <div className="card connections-banner error">
          <span>
            {t("connections.connectFailed", {
              provider: oauthProviderDisplay(oauthProvider).name,
              error: oauthError || t("connections.unknownError"),
            })}
          </span>
          <button type="button" className="link-button" onClick={dismissBanner}>
            {t("common.dismiss")}
          </button>
        </div>
      )}
      {error && <div className="card error">{error}</div>}

      {!providersOff && (
        <div className="connections-providers">
          {providers === null ? (
            <div className="card">{t("common.loading")}</div>
          ) : providers.length === 0 ? (
            <div className="card connections-empty">
              {t("connections.noProviders")}
            </div>
          ) : (
            <div className="provider-grid">
              {providers.map((p) => (
                <ProviderCard
                  key={p.name}
                  provider={p}
                  onConnect={() => connect(p.name)}
                />
              ))}
            </div>
          )}
        </div>
      )}

      {!secretsOff && (
        <CredentialsManager
          secrets={userSecrets}
          loading={secrets === null}
          onChanged={refresh}
        />
      )}
    </div>
  );
}

// ProviderCard renders one OAuth provider: brand + name + what it
// unlocks, the accounts already connected (as chips), and a Connect
// button that adds another account (or the first one).
function ProviderCard({
  provider,
  onConnect,
}: {
  provider: OAuthProviderStatus;
  onConnect: () => void;
}) {
  const { t } = useTranslation();
  const meta = oauthProviderDisplay(provider.name);
  const connected = provider.accounts.length > 0;
  return (
    <div className="provider-card">
      <div className="provider-card-head">
        {meta.brand_logo ? (
          <img
            src={meta.brand_logo}
            alt=""
            className="provider-card-logo"
            draggable={false}
          />
        ) : (
          <span className="provider-card-fallback">
            <Box size={18} strokeWidth={2.2} />
          </span>
        )}
        <div className="provider-card-title">
          <h3>{meta.name}</h3>
          {connected && (
            <span className="provider-connected">
              <Check size={13} strokeWidth={3} /> {t("connections.connected")}
            </span>
          )}
        </div>
      </div>
      {meta.blurb && <p className="provider-card-blurb">{meta.blurb}</p>}
      {connected && (
        <div className="provider-accounts">
          {provider.accounts.map((a) => (
            <span key={a} className="provider-account-chip">
              {a}
            </span>
          ))}
        </div>
      )}
      <button type="button" className="primary provider-connect" onClick={onConnect}>
        {connected
          ? t("connections.connectAnother")
          : t("connections.connect")}
      </button>
    </div>
  );
}

// CredentialsManager lists the hand-entered secrets (DB URLs, API
// tokens) by name — never value, the daemon has no read-back — with
// delete buttons, plus an add form. Used for the ${tenant://NAME} /
// ${env:NAME} references a template needs that aren't OAuth.
function CredentialsManager({
  secrets,
  loading,
  onChanged,
}: {
  secrets: string[];
  loading: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const add = async () => {
    if (!token) return;
    const trimmed = name.trim();
    if (!trimmed || !value) return;
    setBusy(true);
    setErr(null);
    try {
      await api.putSecret(token, trimmed, value);
      setName("");
      setValue("");
      onChanged();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (n: string) => {
    if (!token) return;
    if (!window.confirm(t("connections.deleteConfirm", { name: n }))) return;
    try {
      await api.deleteSecret(token, n);
      onChanged();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    }
  };

  return (
    <div className="credentials">
      <h2 className="credentials-head">{t("connections.credentialsTitle")}</h2>
      <p className="credentials-sub">{t("connections.credentialsIntro")}</p>
      {err && <div className="card error">{err}</div>}
      {loading ? (
        <div className="card">{t("common.loading")}</div>
      ) : secrets.length === 0 ? (
        <p className="credentials-empty">{t("connections.noCredentials")}</p>
      ) : (
        <ul className="credentials-list">
          {secrets.map((n) => (
            <li key={n} className="credentials-item">
              <code>{n}</code>
              <span className="credentials-set">{t("connections.valueSet")}</span>
              <button
                type="button"
                className="icon-button danger"
                aria-label={t("connections.deleteCredential", { name: n })}
                title={t("connections.deleteCredential", { name: n })}
                onClick={() => remove(n)}
              >
                <Trash2 size={15} />
              </button>
            </li>
          ))}
        </ul>
      )}
      <form
        className="credentials-add"
        onSubmit={(e) => {
          e.preventDefault();
          void add();
        }}
      >
        <input
          type="text"
          placeholder={t("connections.namePlaceholder")}
          value={name}
          onChange={(e) => setName(e.target.value)}
          aria-label={t("connections.nameLabel")}
        />
        <input
          type="password"
          placeholder={t("connections.valuePlaceholder")}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          aria-label={t("connections.valueLabel")}
          autoComplete="off"
        />
        <button
          type="submit"
          className="primary"
          disabled={busy || !name.trim() || !value}
        >
          <Plus size={15} /> {busy ? t("connections.saving") : t("connections.addCredential")}
        </button>
      </form>
    </div>
  );
}
