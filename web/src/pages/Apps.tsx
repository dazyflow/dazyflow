import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Box } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { iconFor, isBrandedIcon, categoryColor } from "../icons";
import {
  integrationMeta,
  integrationNameFromSlug,
  integrationSlug,
  oauthProviderDisplay,
} from "../integrationMeta";
import type {
  ConnectionField,
  ConnectionRequirement,
  Manifest,
  OAuthProviderStatus,
} from "../types";

// Apps is the index page — one card per integration ("app") the daemon
// knows about, derived from the live manifest registry plus curated
// prose from integrationMeta. Drops without an Integration field land in
// a "Built-in" bucket so the page covers everything the catalog shows in
// the editor. Cards are grouped into "Ready to use" vs "Needs setup".
export function Apps() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [drops, setDrops] = useState<Manifest[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Connection state, used only to bucket cards into Connected vs
  // Available. A feature being off / forbidden isn't an error here — it
  // just means "nothing is connected", so those fetches fall back to []
  // rather than blocking the page.
  const [secrets, setSecrets] = useState<string[] | null>(null);
  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(null);

  useEffect(() => {
    if (!token) return;
    api
      .listDrops(token)
      .then((r) => setDrops(r.drops))
      .catch((e) => {
        const msg = e instanceof APIError ? `${e.status}: ${e.message}` : (e as Error).message;
        setError(msg);
      });
    api
      .listSecrets(token)
      .then((r) => setSecrets(r.secrets))
      .catch(() => setSecrets([]));
    api
      .listProviders(token)
      .then((r) => setProviders(r.providers))
      .catch(() => setProviders([]));
  }, [token]);

  // Group drops by integration slug. The standard-library bucket
  // catches anything without an Integration field — matches the
  // NodeCatalog grouping rules.
  // Alphabetical by display name (the standard-library bucket shows as
  // "Built-in"). Both sections preserve this order, so one sort covers
  // Ready to use + Needs setup.
  const groups = useMemo(() => {
    const nameOf = (g: { slug: string; meta: { name: string } }) =>
      g.slug === "standard-library" ? t("integrations.builtinGroup") : g.meta.name;
    return buildGroups(drops ?? []).sort((a, b) =>
      nameOf(a).localeCompare(nameOf(b), undefined, { sensitivity: "base" }),
    );
  }, [drops, t]);

  // Split into "Ready to use" (usable right now — no-setup integrations
  // plus connected ones) vs "Needs setup" (declares a connection that
  // isn't configured yet). connectedSlugs drives the green dot, so a
  // connected app still reads distinctly from an always-available one
  // within the Ready section. Re-buckets once secrets/providers load.
  const { ready, needsSetup, connectedSlugs } = useMemo(() => {
    const ready: typeof groups = [];
    const needsSetup: typeof groups = [];
    const connectedSlugs = new Set<string>();
    for (const g of groups) {
      const st = appConnectionState(g.slug, g.drops, secrets, providers);
      (st.needsSetup ? needsSetup : ready).push(g);
      if (st.connected) connectedSlugs.add(g.slug);
    }
    return { ready, needsSetup, connectedSlugs };
  }, [groups, secrets, providers]);

  if (error) {
    return (
      <div className="page">
        <h1>{t("integrations.title")}</h1>
        <div className="card error">{error}</div>
      </div>
    );
  }
  if (!drops) {
    return (
      <div className="page">
        <h1>{t("integrations.title")}</h1>
        <div className="card">{t("common.loading")}</div>
      </div>
    );
  }

  return (
    <div className="page integrations-page">
      <h1>{t("integrations.title")}</h1>
      <p className="page-sub">{t("integrations.intro")}</p>
      {ready.length > 0 && (
        <>
          <h2 className="integrations-section-head">{t("integrations.readyHead")}</h2>
          <div className="integration-grid">
            {ready.map((g) => (
              <IntegrationCard key={g.slug} {...g} connected={connectedSlugs.has(g.slug)} />
            ))}
          </div>
        </>
      )}
      {needsSetup.length > 0 && (
        <>
          <h2 className="integrations-section-head">{t("integrations.needsSetupHead")}</h2>
          <div className="integration-grid">
            {needsSetup.map((g) => (
              <IntegrationCard key={g.slug} {...g} connected={false} />
            ))}
          </div>
        </>
      )}
    </div>
  );
}

// IntegrationCard is one tile in the index grid. `connected` shows a
// green status dot next to the name — the at-a-glance "this is set up"
// signal that pairs with the Connected section heading.
function IntegrationCard({
  slug,
  meta,
  drops,
  connected,
}: {
  slug: string;
  meta: { name: string; description: string; brand_logo?: string };
  drops: Manifest[];
  connected: boolean;
}) {
  const { t } = useTranslation();
  // Logo fallback chain: curated override → any drop's brand_logo →
  // category-derived lucide glyph from the first drop. Drops carrying
  // their own brand_logo means we render the right vendor mark even for
  // integrations without a curated metadata entry (excel, mysql,
  // postgres, sqlite all ship per-drop logos).
  const brandLogo = meta.brand_logo ?? drops.find((d) => d.brand_logo)?.brand_logo;
  const headerDrop = drops[0];
  const HeaderIcon = headerDrop ? iconFor(headerDrop.icon, headerDrop.category) : Box;
  const headerBranded = isBrandedIcon(headerDrop?.icon);
  return (
    <Link
      to={`/apps/${encodeURIComponent(slug)}`}
      style={{ textDecoration: "none", color: "inherit" }}
    >
      <div className="integration-card">
        <div className="integration-card-head">
          {brandLogo ? (
            <img
              src={brandLogo}
              alt=""
              className="integration-card-logo"
              draggable={false}
            />
          ) : (
            <span className="integration-card-fallback-icon">
              <HeaderIcon size={headerBranded ? 22 : 18} strokeWidth={2.2} />
            </span>
          )}
          <h2>
            {slug === "standard-library" ? t("integrations.builtinGroup") : meta.name}
          </h2>
          {connected && (
            <span
              className="connection-dot on integration-card-dot"
              title={t("integrations.connectedTip")}
            />
          )}
        </div>
        <p className="integration-card-desc">{meta.description}</p>
      </div>
    </Link>
  );
}

// appConnectionState classifies an integration for the index grouping.
// needsSetup = it declares a connection (single secret, OAuth, or a
// multi-field service connection) that isn't fully satisfied yet.
// connected = it declares one and it IS satisfied. A no-connection
// integration is neither (needsSetup:false, connected:false) — always
// "Ready to use", no dot.
function appConnectionState(
  slug: string,
  drops: Manifest[],
  secrets: string[] | null,
  providers: OAuthProviderStatus[] | null,
): { needsSetup: boolean; connected: boolean } {
  const reqs = dedupeRequirements(drops);
  const fields = drops.find((d) => d.connection_fields?.length)?.connection_fields ?? [];
  if (reqs.length === 0 && fields.length === 0) {
    return { needsSetup: false, connected: false };
  }
  const reqsOk = reqs.every((req) =>
    req.kind === "secret"
      ? (secrets ?? []).includes(req.name)
      : ((providers ?? []).find((p) => p.name === req.name)?.accounts.length ?? 0) > 0,
  );
  let fieldsOk = true;
  if (fields.length > 0) {
    const required = fields.filter((f) => f.required);
    const isSet = (f: ConnectionField) => (secrets ?? []).includes(`conn.${slug}.${f.key}`);
    fieldsOk = required.length > 0 ? required.every(isSet) : fields.some(isSet);
  }
  const connected = reqsOk && fieldsOk;
  return { needsSetup: !connected, connected };
}

// AppDetail is /apps/:slug — the per-app "profile" page. Shows
// the hero (logo + name + full prose), the Connection card(s), and every
// drop the app ships with its ports and a collapsed params hint.
export function AppDetail() {
  const { t } = useTranslation();
  const slugRaw = window.location.pathname.split("/").pop() ?? "";
  const slug = decodeURIComponent(slugRaw);
  const { token, hasPerm } = useAuth();
  // Connecting/managing an app needs secret:write. Viewers can browse the
  // catalog + drops, but the whole connection section is hidden for them
  // (no read-only card, no "ask an admin" note) — it's not theirs to act on.
  const canManageConnections = hasPerm("secret:write");
  const [drops, setDrops] = useState<Manifest[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    api
      .listDrops(token)
      .then((r) => setDrops(r.drops))
      .catch((e) => {
        const msg = e instanceof APIError ? `${e.status}: ${e.message}` : (e as Error).message;
        setError(msg);
      });
  }, [token]);

  const { meta, integrationDrops } = useMemo(() => {
    const all = drops ?? [];
    const filtered = all.filter((m) => integrationSlugFor(m) === slug);
    const base = integrationMeta[slug] ?? {
      name: integrationNameFromSlug(slug),
      description: "",
    };
    // The catch-all bucket reads as "Built-in" to non-technical users,
    // not "Standard Library".
    const m =
      slug === "standard-library"
        ? { ...base, name: t("integrations.builtinGroup") }
        : base;
    return { meta: m, integrationDrops: filtered };
  }, [drops, slug, t]);

  if (error) {
    return (
      <div className="page">
        <h1>{meta.name}</h1>
        <div className="card error">{error}</div>
      </div>
    );
  }
  if (!drops) {
    return (
      <div className="page">
        <div className="card">{t("common.loading")}</div>
      </div>
    );
  }
  if (integrationDrops.length === 0) {
    return (
      <div className="page">
        <h1>{meta.name}</h1>
        <Link to="/apps" className="back-link">
          {t("integrations.backAll")}
        </Link>
        <div className="card" style={{ marginTop: 12 }}>
          {t("integrations.noDrops")}
        </div>
      </div>
    );
  }

  // Pick a brand logo: curated override wins, otherwise borrow from
  // the first drop with one.
  const brandLogo =
    meta.brand_logo ?? integrationDrops.find((d) => d.brand_logo)?.brand_logo;

  return (
    <div className="page integration-detail">
      <Link to="/apps" className="back-link">
        {t("integrations.backAll")}
      </Link>
      <header className="integration-hero">
        {brandLogo && (
          <img
            src={brandLogo}
            alt=""
            className="integration-hero-logo"
            draggable={false}
          />
        )}
        <div>
          <h1>{meta.name}</h1>
          {meta.description && (
            <p className="integration-hero-desc">{meta.description}</p>
          )}
          {meta.docs_url && (
            <p className="integration-hero-docs">
              <a href={meta.docs_url} target="_blank" rel="noreferrer noopener">
                {t("integrations.officialDocs")}
              </a>
            </p>
          )}
        </div>
      </header>

      {canManageConnections && (
        <IntegrationConnections drops={integrationDrops} slug={slug} name={meta.name} />
      )}

      <h2 className="integration-drops-head">{t("integrations.dropsHead")}</h2>
      <div className="integration-drops">
        {integrationDrops.map((d) => (
          <DropCard key={d.id} drop={d} />
        ))}
      </div>
    </div>
  );
}

// featureUnavailable: a 501 (not configured), 401/403 (not permitted)
// from a connections endpoint means "this feature isn't usable for
// this caller" — render an inert "ask your admin" note rather than an
// error banner. Mirrors the same helper on the Connections page.
function featureUnavailable(status: number): boolean {
  return status === 501 || status === 401 || status === 403;
}

// IntegrationConnections is the configure widget: it turns each drop's
// requires_connections into an actionable row, so a user sets up an
// integration here (enter the API key / Connect the account) instead
// of having to know the magic secret name on the raw Credentials list.
// Secret-kind requirements get an inline key field that writes to the
// manifest-declared name; oauth-kind requirements reuse the Connect
// redirect (return_to bounces back to this page). Renders nothing when
// the integration declares no connection requirements (e.g. the
// standard library), so it's inert for everything that needs no auth.
function IntegrationConnections({
  drops,
  slug,
  name,
}: {
  drops: Manifest[];
  slug: string;
  name: string;
}) {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const canWrite = hasPerm("secret:write");
  const [searchParams, setSearchParams] = useSearchParams();

  const reqs = useMemo(() => dedupeRequirements(drops), [drops]);
  // A multi-field service connection (ntfy server+token, SMTP host/…) is
  // declared identically across the integration's drops; take the first.
  const connectionFields = useMemo(
    () => drops.find((d) => d.connection_fields?.length)?.connection_fields ?? [],
    [drops],
  );
  const needsSecret = reqs.some((r) => r.kind === "secret") || connectionFields.length > 0;
  const needsOAuth = reqs.some((r) => r.kind === "oauth");

  const [secrets, setSecrets] = useState<string[] | null>(null);
  const [secretsOff, setSecretsOff] = useState(false);
  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(null);
  const [providersOff, setProvidersOff] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = () => {
    if (!token) return;
    if (needsSecret) {
      api
        .listSecrets(token)
        .then((r) => {
          setSecrets(r.secrets);
          setSecretsOff(false);
        })
        .catch((e) => {
          if (e instanceof APIError && featureUnavailable(e.status)) setSecretsOff(true);
          else setError(e instanceof APIError ? e.message : (e as Error).message);
        });
    }
    if (needsOAuth) {
      api
        .listProviders(token)
        .then((r) => {
          setProviders(r.providers);
          setProvidersOff(false);
        })
        .catch((e) => {
          if (e instanceof APIError && featureUnavailable(e.status)) setProvidersOff(true);
          else setError(e instanceof APIError ? e.message : (e as Error).message);
        });
    }
  };
  // reqs is derived from drops (stable per detail load), so token is the
  // only real dependency; needsSecret/needsOAuth are read inside refresh.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(refresh, [token, slug]);

  // OAuth bounces back to /apps/:slug?oauth=success|error. Show
  // the result once, then strip the params so a refresh doesn't re-show.
  const oauthResult = searchParams.get("oauth");
  const oauthError = searchParams.get("error") ?? "";
  const dismissBanner = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("oauth");
    next.delete("provider");
    next.delete("account");
    next.delete("error");
    setSearchParams(next, { replace: true });
  };

  if (reqs.length === 0 && connectionFields.length === 0) return null;

  return (
    <section className="integration-connections">
      {oauthResult === "error" && (
        <div className="card connections-banner error">
          <span>
            {t("integrations.connection.connectFailed", {
              error: oauthError || t("connections.unknownError"),
            })}
          </span>
          <button type="button" className="link-button" onClick={dismissBanner}>
            {t("common.dismiss")}
          </button>
        </div>
      )}
      {error && <div className="card error">{error}</div>}
      {connectionFields.length > 0 && (
        <ConnectionFieldsCard
          fields={connectionFields}
          name={name}
          slug={slug}
          secrets={secrets}
          loading={secrets === null && !secretsOff}
          off={secretsOff}
          canWrite={canWrite}
          onChanged={refresh}
        />
      )}
      {reqs.map((req) =>
        req.kind === "secret" ? (
          <SecretCard
            key={`secret:${req.name}`}
            req={req}
            name={name}
            configured={secrets?.includes(req.name) ?? false}
            loading={secrets === null && !secretsOff}
            off={secretsOff}
            canWrite={canWrite}
            onChanged={refresh}
          />
        ) : (
          <OAuthCard
            key={`oauth:${req.name}`}
            req={req}
            status={providers?.find((p) => p.name === req.name) ?? null}
            loading={providers === null && !providersOff}
            off={providersOff}
            canWrite={canWrite}
            slug={slug}
          />
        ),
      )}
    </section>
  );
}

// ConnectionStatus is the card's headline — a status dot plus
// "Connected to X" / "Connect X" — the at-a-glance state, shared by the
// secret and oauth cards.
function ConnectionStatus({
  connected,
  title,
}: {
  connected: boolean;
  title: string;
}) {
  return (
    <div className="connection-card-status">
      <span className={"connection-dot " + (connected ? "on" : "off")} />
      <h3>{title}</h3>
    </div>
  );
}

// SecretCard is the heart of the widget: a card titled by the
// integration with an inline key field. The user pastes the value into
// a field labelled by the manifest's note ("Anthropic API key") — they
// never type the secret NAME. Saving writes under the declared name so
// existing ${secret.NAME} references resolve.
function SecretCard({
  req,
  name,
  configured,
  loading,
  off,
  canWrite,
  onChanged,
}: {
  req: ConnectionRequirement;
  name: string;
  configured: boolean;
  loading: boolean;
  off: boolean;
  canWrite: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [value, setValue] = useState("");
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Split the manifest note into a field label and a placeholder hint:
  // "Anthropic API key (sk-ant-…)." → label "Anthropic API key",
  // placeholder "sk-ant-…". Notes without a parenthetical fall back to
  // the whole note as the label and a generic placeholder.
  const note = req.note ?? req.name;
  const paren = note.match(/^(.*?)\s*\(([^)]*)\)\s*\.?$/);
  const fieldLabel = (paren ? paren[1] : note.replace(/\.$/, "")).trim();
  const placeholder = paren ? paren[2] : t("integrations.connection.valuePlaceholder");

  const save = async () => {
    if (!token || !value) return;
    setBusy(true);
    setErr(null);
    try {
      await api.putSecret(token, req.name, value);
      setValue("");
      setEditing(false);
      onChanged();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!token) return;
    if (!window.confirm(t("integrations.connection.removeConfirm", { name: req.name }))) return;
    try {
      await api.deleteSecret(token, req.name);
      onChanged();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    }
  };

  return (
    <div className="connection-card">
      <ConnectionStatus
        connected={configured}
        title={
          configured
            ? t("integrations.connection.connectedTo", { name })
            : t("integrations.connection.connectPrompt", { name })
        }
      />
      {off ? (
        <p className="connection-note">{t("integrations.connection.storeOff")}</p>
      ) : loading ? (
        <p className="connection-note">{t("common.loading")}</p>
      ) : configured && !editing ? (
        <>
          <label className="connection-field">
            <span className="connection-field-label">{fieldLabel}</span>
            <input type="password" value="••••••••••" readOnly aria-label={fieldLabel} />
          </label>
          {err && <div className="card error">{err}</div>}
          {canWrite && (
            <div className="connection-card-footer">
              <button type="button" className="ghost" onClick={() => setEditing(true)}>
                {t("integrations.connection.edit")}
              </button>
              <button type="button" className="danger-outline" onClick={() => void remove()}>
                {t("integrations.connection.disconnect")}
              </button>
            </div>
          )}
        </>
      ) : canWrite && (!configured || editing) ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <label className="connection-field">
            <span className="connection-field-label">{fieldLabel}</span>
            <input
              type="password"
              placeholder={placeholder}
              value={value}
              onChange={(e) => setValue(e.target.value)}
              autoComplete="off"
            />
          </label>
          {err && <div className="card error">{err}</div>}
          <div className="connection-card-footer">
            <button type="submit" className="primary" disabled={busy || !value}>
              {busy ? t("connections.saving") : t("connections.connect")}
            </button>
            {configured && (
              <button
                type="button"
                className="ghost"
                onClick={() => {
                  setEditing(false);
                  setValue("");
                }}
              >
                {t("common.cancel")}
              </button>
            )}
          </div>
        </form>
      ) : (
        <p className="connection-note">{t("integrations.connection.notConfigured")}</p>
      )}
    </div>
  );
}

// OAuthCard surfaces an oauth-kind requirement as the same card. The
// status comes from GET /oauth/providers; Connect does the full-page
// authorize redirect, with return_to set to this page so the round-trip
// lands back here.
function OAuthCard({
  req,
  status,
  loading,
  off,
  canWrite,
  slug,
}: {
  req: ConnectionRequirement;
  status: OAuthProviderStatus | null;
  loading: boolean;
  off: boolean;
  canWrite: boolean;
  slug: string;
}) {
  const { t } = useTranslation();
  const meta = oauthProviderDisplay(req.name);
  const connected = (status?.accounts.length ?? 0) > 0;
  const connect = () => {
    window.location.assign(
      api.oauthAuthorizeUrl(req.name, `/apps/${encodeURIComponent(slug)}`),
    );
  };

  return (
    <div className="connection-card">
      <ConnectionStatus
        connected={connected}
        title={
          connected
            ? t("integrations.connection.connectedTo", { name: meta.name })
            : t("integrations.connection.connectPrompt", { name: meta.name })
        }
      />
      {off ? (
        <p className="connection-note">{t("integrations.connection.oauthOff")}</p>
      ) : loading ? (
        <p className="connection-note">{t("common.loading")}</p>
      ) : canWrite ? (
        <div className="connection-card-footer">
          <button type="button" className="primary" onClick={connect}>
            {connected ? t("connections.connectAnother") : t("connections.connect")}
          </button>
        </div>
      ) : !connected ? (
        // Viewer (no secret:write) + not connected: mirror the secret
        // cards' "ask an admin" note instead of a bare headline.
        <p className="connection-note">{t("integrations.connection.notConfigured")}</p>
      ) : null}
    </div>
  );
}

// ConnectionFieldsCard is the multi-field service connection — an
// endpoint + credentials a tenant sets once (ntfy server+token, SMTP
// host/user/pass) so flows only carry per-use params. Each field is
// stored as the tenant secret conn/<slug>/<key>; secret fields are
// password inputs, plain fields (a URL) are text. "Connected" means
// every required field is set (or, when nothing is required, at least
// one field is set). The engine injects these into a node's unset
// params at run time — see core.injectConnectionDefaults.
function ConnectionFieldsCard({
  fields,
  name,
  slug,
  secrets,
  loading,
  off,
  canWrite,
  onChanged,
}: {
  fields: ConnectionField[];
  name: string;
  slug: string;
  secrets: string[] | null;
  loading: boolean;
  off: boolean;
  canWrite: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [values, setValues] = useState<Record<string, string>>({});
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const keyFor = (f: ConnectionField) => `conn.${slug}.${f.key}`;
  const isSet = (f: ConnectionField) => secrets?.includes(keyFor(f)) ?? false;
  const required = fields.filter((f) => f.required);
  const connected =
    required.length > 0 ? required.every(isSet) : fields.some(isSet);

  const save = async () => {
    if (!token) return;
    const pending = fields.filter((f) => (values[f.key] ?? "").trim() !== "");
    if (pending.length === 0) return;
    setBusy(true);
    setErr(null);
    try {
      for (const f of pending) {
        await api.putSecret(token, keyFor(f), values[f.key]);
      }
      setValues({});
      setEditing(false);
      onChanged();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const disconnect = async () => {
    if (!token) return;
    if (!window.confirm(t("integrations.connection.disconnectFieldsConfirm", { name }))) return;
    try {
      for (const f of fields) {
        if (isSet(f)) await api.deleteSecret(token, keyFor(f));
      }
      onChanged();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    }
  };

  const showForm = canWrite && (!connected || editing);

  return (
    <div className="connection-card">
      <ConnectionStatus
        connected={connected}
        title={
          connected
            ? t("integrations.connection.connectedTo", { name })
            : t("integrations.connection.connectPrompt", { name })
        }
      />
      {off ? (
        <p className="connection-note">{t("integrations.connection.storeOff")}</p>
      ) : loading ? (
        <p className="connection-note">{t("common.loading")}</p>
      ) : connected && !editing ? (
        <>
          <ul className="connection-fields-summary">
            {fields.map((f) => (
              <li key={f.key}>
                <span className="connection-field-label">{f.label}</span>
                <span className={isSet(f) ? "credentials-set" : "connection-note"}>
                  {isSet(f)
                    ? f.secret
                      ? "••••••••"
                      : t("integrations.connection.fieldSet")
                    : t("integrations.connection.fieldUnset")}
                </span>
              </li>
            ))}
          </ul>
          {err && <div className="card error">{err}</div>}
          {canWrite && (
            <div className="connection-card-footer">
              <button type="button" className="ghost" onClick={() => setEditing(true)}>
                {t("integrations.connection.edit")}
              </button>
              <button type="button" className="danger-outline" onClick={() => void disconnect()}>
                {t("integrations.connection.disconnect")}
              </button>
            </div>
          )}
        </>
      ) : showForm ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          {fields.map((f) => (
            <label className="connection-field" key={f.key}>
              <span className="connection-field-label">
                {f.label}
                {isSet(f) && (
                  <span className="connection-field-set-hint"> · {t("integrations.connection.fieldSet")}</span>
                )}
              </span>
              <input
                type={f.secret ? "password" : "text"}
                placeholder={f.placeholder ?? ""}
                value={values[f.key] ?? ""}
                onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
                autoComplete="off"
              />
            </label>
          ))}
          {err && <div className="card error">{err}</div>}
          <div className="connection-card-footer">
            <button type="submit" className="primary" disabled={busy}>
              {busy ? t("connections.saving") : t("connections.connect")}
            </button>
            {connected && (
              <button
                type="button"
                className="ghost"
                onClick={() => {
                  setEditing(false);
                  setValues({});
                }}
              >
                {t("common.cancel")}
              </button>
            )}
          </div>
        </form>
      ) : (
        <p className="connection-note">{t("integrations.connection.notConfigured")}</p>
      )}
    </div>
  );
}

// dedupeRequirements flattens requires_connections across every drop in
// the integration into a unique list keyed by (kind, name) — Notion's
// two drops both want NOTION_TOKEN, Google's many drops all ride one
// "google" oauth — keeping the first note seen. Secret-kind first (the
// gap this widget fills), then oauth; name-sorted within each.
function dedupeRequirements(drops: Manifest[]): ConnectionRequirement[] {
  const seen = new Map<string, ConnectionRequirement>();
  for (const d of drops) {
    for (const req of d.requires_connections ?? []) {
      const key = `${req.kind}:${req.name}`;
      if (!seen.has(key)) seen.set(key, req);
    }
  }
  return [...seen.values()].sort((a, b) =>
    a.kind === b.kind ? a.name.localeCompare(b.name) : a.kind === "secret" ? -1 : 1,
  );
}

// DropCard renders one drop's "help" entry: icon + label + module
// ID, full description, input + output ports, and a collapsed view
// of the params schema (rendered as a JSON dump under a <details>).
function DropCard({ drop }: { drop: Manifest }) {
  const { t } = useTranslation();
  const Icon = iconFor(drop.icon, drop.category);
  const branded = isBrandedIcon(drop.icon);
  const color = drop.color || categoryColor(drop.category) || "#9f83fe";
  return (
    <div className="drop-card">
      <div className="drop-card-head">
        {drop.brand_logo ? (
          <div className="icon brand-logo">
            <img src={drop.brand_logo} alt="" draggable={false} />
          </div>
        ) : branded ? (
          <div className="icon branded">
            <Icon size={22} strokeWidth={2.2} />
          </div>
        ) : (
          <div
            className="icon"
            style={{
              background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
            }}
          >
            <Icon size={16} color="#140d30" strokeWidth={2.2} />
          </div>
        )}
        <div className="drop-card-title">
          <h3>{drop.label}</h3>
          <code className="drop-card-id">{drop.id}</code>
        </div>
      </div>
      {drop.description && (
        <p className="drop-card-desc">{drop.description}</p>
      )}
      {/* Wiring + params live behind one disclosure so the
          visible-by-default surface is "what this drop is for";
          one click expands to "how do I connect it." Keeps non-
          technical scanners focused without hiding anything from
          a developer who clicks through. */}
      {hasWiringDetails(drop) && (
        <details className="drop-card-wiring">
          <summary>{t("integrations.wiringDetails")}</summary>
          <div className="drop-card-ports">
            {drop.inputs && drop.inputs.length > 0 && (
              <div>
                <div className="drop-card-port-head">{t("integrations.inputs")}</div>
                <ul>
                  {drop.inputs.map((p) => (
                    <li key={p.port}>
                      <code>{p.port}</code>
                      {p.required && (
                        <span className="port-required"> {t("integrations.required")}</span>
                      )}
                      {p.label && (
                        <span className="port-label"> — {p.label}</span>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {drop.outputs && drop.outputs.length > 0 && (
              <div>
                <div className="drop-card-port-head">{t("integrations.outputs")}</div>
                <ul>
                  {drop.outputs.map((p) => (
                    <li key={p.port}>
                      <code>{p.port}</code>
                      {p.label && (
                        <span className="port-label"> — {p.label}</span>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
          {drop.params_schema && (
            <div className="drop-card-params-block">
              <div className="drop-card-port-head">{t("integrations.paramsSchema")}</div>
              <pre>{JSON.stringify(drop.params_schema, null, 2)}</pre>
            </div>
          )}
        </details>
      )}
    </div>
  );
}

// hasWiringDetails reports whether a drop has any of the
// technical-flavored fields the disclosure would reveal. Drops
// with no inputs, no outputs, and no params schema would render an
// empty disclosure — skip the summary entirely in that case so
// the card stays clean.
function hasWiringDetails(d: Manifest): boolean {
  return (
    (d.inputs && d.inputs.length > 0) ||
    (d.outputs && d.outputs.length > 0) ||
    !!d.params_schema
  ) as boolean;
}

// integrationSlugFor returns the slug a drop belongs to. Drops
// without an Integration field land in "standard-library" — same
// rule the NodeCatalog uses for grouping.
function integrationSlugFor(m: Manifest): string {
  if (m.integration && m.integration.trim() !== "") {
    return integrationSlug(m.integration);
  }
  return "standard-library";
}

// buildGroups maps drops into the displayable group list. The
// curated integrationMeta entries are surfaced first (in
// declaration order) so the most-polished integrations appear at
// the top; any uncurated slugs that still have drops get tacked on
// alphabetically at the end.
function buildGroups(all: Manifest[]) {
  const bySlug = new Map<string, Manifest[]>();
  for (const m of all) {
    const slug = integrationSlugFor(m);
    const arr = bySlug.get(slug) ?? [];
    arr.push(m);
    bySlug.set(slug, arr);
  }
  const out: Array<{
    slug: string;
    meta: { name: string; description: string; brand_logo?: string };
    drops: Manifest[];
  }> = [];
  const seen = new Set<string>();
  for (const slug of Object.keys(integrationMeta)) {
    if (!bySlug.has(slug)) continue;
    out.push({ slug, meta: integrationMeta[slug], drops: bySlug.get(slug)! });
    seen.add(slug);
  }
  const tail = Array.from(bySlug.keys())
    .filter((s) => !seen.has(s))
    .sort();
  for (const slug of tail) {
    out.push({
      slug,
      meta: {
        name: integrationNameFromSlug(slug),
        description: "",
      },
      drops: bySlug.get(slug)!,
    });
  }
  return out;
}
