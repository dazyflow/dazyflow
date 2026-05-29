import { useEffect, useMemo, useRef, useState } from "react";
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
  const { token, me } = useAuth();
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
        <SetupIncompleteBanner supportContact={me?.support_contact} />
      )}

      {/* First-time tile: features are configured server-side, but
          this tenant has nothing connected and no credentials stored
          yet. Surface the two integrations most non-tech buyers will
          want first so they don't stare at a wall of provider cards
          wondering which to click. Hidden once anything is in place,
          or when either feature is off (the SetupIncompleteBanner
          covers that case instead). */}
      {!providersOff && !secretsOff &&
        providers !== null &&
        providers.length > 0 &&
        providers.every((p) => p.accounts.length === 0) &&
        userSecrets.length === 0 && (
          <FirstConnectTile />
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
          // The editor's "Set up this credential" links route to
          // /connections?focus=NAME so a user landing here from a
          // template field knows which credential they need to add.
          // Consumed once on mount: the credentials manager scrolls
          // + highlights an existing row or pre-fills the add-form;
          // we strip the param afterwards so a refresh doesn't
          // re-fire the highlight.
          focus={searchParams.get("focus") ?? undefined}
          onFocusConsumed={() => {
            const next = new URLSearchParams(searchParams);
            next.delete("focus");
            setSearchParams(next, { replace: true });
          }}
        />
      )}
    </div>
  );
}

// FirstConnectTile is the "you've just landed and nothing is wired
// up yet" prompt. Names the two integrations that satisfy the
// largest share of templates (Slack for notifications, Google for
// Gmail + Sheets) so a non-tech buyer knows where to start instead
// of evaluating every provider card. The tile disappears the
// moment something is connected.
function FirstConnectTile() {
  const { t } = useTranslation();
  return (
    <div className="card connections-first-tile">
      <h2 className="connections-first-tile-title">
        {t("connections.firstTileTitle")}
      </h2>
      <p className="connections-first-tile-body">
        {t("connections.firstTileBody")}
      </p>
    </div>
  );
}

// SetupIncompleteBanner replaces the bare "feature off" card when BOTH
// OAuth and the encrypted secret store come back unavailable. The
// page would otherwise be empty save the title — leaving a paying
// end-user with no path forward. The banner names the situation,
// pins the responsibility on the operator (not the end user), and
// gives them somewhere to click when a support contact is set.
function SetupIncompleteBanner({
  supportContact,
}: {
  supportContact?: string;
}) {
  const { t } = useTranslation();
  const href = supportContactHref(supportContact);
  return (
    <div className="card connections-setup-incomplete" role="status">
      <h2 className="connections-setup-incomplete-title">
        {t("connections.setupIncompleteTitle")}
      </h2>
      <p>{t("connections.setupIncompleteBody")}</p>
      {href ? (
        <a className="primary" href={href}>
          {t("connections.setupIncompleteContact")}
        </a>
      ) : (
        <p className="connections-setup-incomplete-fallback">
          {t("connections.setupIncompleteContactGeneric")}
        </p>
      )}
    </div>
  );
}

// supportContactHref turns an operator-set contact string into the
// right `href`. We accept three shapes so the operator doesn't have
// to think about escaping:
//   - "support@acme.com"          → mailto:support@acme.com
//   - "https://acme.com/help"     → as-is
//   - "http://acme.com/help"      → as-is
// Anything else returns undefined, which falls back to the generic
// "ask your admin" copy (no clickable link).
function supportContactHref(raw?: string): string | undefined {
  const trimmed = raw?.trim();
  if (!trimmed) return undefined;
  if (trimmed.startsWith("http://") || trimmed.startsWith("https://")) {
    return trimmed;
  }
  if (trimmed.startsWith("mailto:")) return trimmed;
  // Email heuristic: `local@domain` with no whitespace. Good enough
  // for the operator-input use case; this isn't an RFC validator.
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)) {
    return `mailto:${trimmed}`;
  }
  return undefined;
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
          {provider.accounts.map((a) => {
            const stale = provider.stale_accounts?.includes(a) ?? false;
            return (
              <span
                key={a}
                className={
                  "provider-account-chip" + (stale ? " provider-account-chip-stale" : "")
                }
                title={
                  stale ? t("connections.reconnectRequiredHint") : undefined
                }
              >
                {a}
                {stale && (
                  <span className="provider-account-stale-pill">
                    {t("connections.reconnectRequired")}
                  </span>
                )}
              </span>
            );
          })}
        </div>
      )}
      <button type="button" className="primary provider-connect" onClick={onConnect}>
        {connected
          ? (provider.stale_accounts?.length
              ? t("connections.reconnect")
              : t("connections.connectAnother"))
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
  focus,
  onFocusConsumed,
}: {
  secrets: string[];
  loading: boolean;
  onChanged: () => void;
  // focus is a credential name the user was pointed at from
  // somewhere else (a template field's "Set up" link). When the
  // secret already exists, the matching row scrolls into view and
  // highlights briefly; otherwise the add-form pre-fills with the
  // name + the value input takes focus so the user can finish
  // setting it up in one step. onFocusConsumed strips the query
  // param so a refresh doesn't re-fire the highlight.
  focus?: string;
  onFocusConsumed?: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [highlighted, setHighlighted] = useState<string | null>(null);
  const valueInputRef = useRef<HTMLInputElement | null>(null);
  const rowRefs = useRef<Map<string, HTMLLIElement | null>>(new Map());

  // Apply the inbound ?focus= once. We wait until the secrets list
  // has actually loaded (so we know whether the credential exists
  // or not) — calling onFocusConsumed only after we've acted means
  // a slow secrets fetch doesn't drop the focus on the floor.
  useEffect(() => {
    if (!focus || loading) return;
    if (secrets.includes(focus)) {
      // Existing row — scroll to it and pulse the highlight class.
      const row = rowRefs.current.get(focus);
      row?.scrollIntoView({ behavior: "smooth", block: "center" });
      setHighlighted(focus);
      const handle = window.setTimeout(() => setHighlighted(null), 2000);
      onFocusConsumed?.();
      return () => window.clearTimeout(handle);
    }
    // New credential — pre-fill the name, focus the value input so
    // the user can type the secret without an extra click.
    setName(focus);
    requestAnimationFrame(() => valueInputRef.current?.focus());
    onFocusConsumed?.();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus, loading, secrets.join("\0")]);

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
            <li
              key={n}
              ref={(el) => {
                rowRefs.current.set(n, el);
              }}
              className={
                "credentials-item" +
                (highlighted === n ? " credentials-item-highlight" : "")
              }
            >
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
          ref={valueInputRef}
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
