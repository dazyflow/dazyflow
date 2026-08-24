// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Box } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { useAuth } from "../auth";
import { iconFor, isBrandedIcon, dropColor } from "../icons";
import {
  connectionText,
  integrationProse,
  dropCategoryLabel,
  dropDescription,
  dropLabel,
  dropSubtitle,
  portLabel,
} from "../lib/dropText";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";
import {
  integrationMeta,
  integrationNameFromSlug,
  integrationSlug,
  oauthProviderDisplay,
} from "../integrationMeta";
import { ErrorNotice } from "../components/ErrorNotice";
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
  const { t, i18n } = useTranslation();
  const { token } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
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
    // Cancelled-flag guard: a late response from a previous token (sign-out /
    // tenant switch) must not overwrite the current page's state. Mirrors the
    // pattern used across the run pages.
    let cancelled = false;
    api
      .listDrops(token)
      .then((r) => {
        if (!cancelled) setDrops(r.drops);
      })
      .catch((e) => {
        if (cancelled) return;
        setError(explainApiError(e, t));
      });
    api
      .listSecrets(token, undefined, undefined, true)
      .then((r) => {
        if (!cancelled) setSecrets(r.secrets);
      })
      .catch(() => {
        if (!cancelled) setSecrets([]);
      });
    api
      .listProviders(token)
      .then((r) => {
        if (!cancelled) setProviders(r.providers);
      })
      .catch(() => {
        if (!cancelled) setProviders([]);
      });
    return () => {
      cancelled = true;
    };
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
  const { ready, needsSetup, connectedSlugs, ailingSlugs } = useMemo(() => {
    const ready: typeof groups = [];
    const needsSetup: typeof groups = [];
    const connectedSlugs = new Set<string>();
    const ailingSlugs = new Set<string>();
    for (const g of groups) {
      const st = appConnectionState(g.slug, g.drops, secrets, providers);
      (st.needsSetup ? needsSetup : ready).push(g);
      if (st.connected) connectedSlugs.add(g.slug);
      if (st.ailing) ailingSlugs.add(g.slug);
    }
    return { ready, needsSetup, connectedSlugs, ailingSlugs };
  }, [groups, secrets, providers]);

  // Optional ?category= filter narrows the index to integrations whose
  // catalog steps carry that drop category. The "Connect Claude or ChatGPT"
  // entry points deep-link here with ?category=ai so the page opens scoped
  // to just the AI providers. An empty/absent param shows everything.
  const categoryFilter = searchParams.get("category");
  const inCategory = (g: (typeof groups)[number]) =>
    g.drops.some((d) => d.category === categoryFilter);
  const filteredReady = categoryFilter ? ready.filter(inCategory) : ready;
  const filteredNeedsSetup = categoryFilter
    ? needsSetup.filter(inCategory)
    : needsSetup;
  const filterLabel = categoryFilter
    ? categoryLabel(categoryFilter, t, i18n.language)
    : "";
  const clearFilter = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("category");
    setSearchParams(next, { replace: true });
  };

  if (error) {
    return (
      <div className="page">
        <h1>{t("integrations.title")}</h1>
        <ErrorNotice>{error}</ErrorNotice>
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

  const filterEmpty =
    categoryFilter &&
    filteredReady.length === 0 &&
    filteredNeedsSetup.length === 0;

  return (
    <div className="page integrations-page">
      <h1>{t("integrations.title")}</h1>
      <p className="page-sub">{t("integrations.intro")}</p>
      {categoryFilter && (
        <div className="integrations-filter">
          <span className="integrations-filter-chip">
            {t("integrations.filterShowing", { label: filterLabel })}
            <Button
              className="integrations-filter-clear"
              onClick={clearFilter}
            >
              {t("integrations.filterClear")}
            </Button>
          </span>
        </div>
      )}
      {filterEmpty && (
        <div className="card">
          {t("integrations.filterEmpty", { label: filterLabel })}
        </div>
      )}
      {filteredReady.length > 0 && (
        <>
          <h2 className="integrations-section-head">{t("integrations.readyHead")}</h2>
          <div className="integration-grid">
            {filteredReady.map((g) => (
              <IntegrationCard
                key={g.slug}
                {...g}
                connected={connectedSlugs.has(g.slug)}
                ailing={ailingSlugs.has(g.slug)}
              />
            ))}
          </div>
        </>
      )}
      {filteredNeedsSetup.length > 0 && (
        <>
          <h2 className="integrations-section-head">{t("integrations.needsSetupHead")}</h2>
          <div className="integration-grid">
            {filteredNeedsSetup.map((g) => (
              <IntegrationCard key={g.slug} {...g} connected={false} ailing={false} />
            ))}
          </div>
        </>
      )}
    </div>
  );
}

// categoryLabel maps a drop category to a friendly filter label. Only the
// curated categories get prose; anything else falls back to the raw value
// (capitalized) so a new category still renders something sensible.
function categoryLabel(
  category: string,
  t: (k: string) => string,
  lang?: string,
): string {
  if (category === "ai") return t("integrations.categoryAi");
  // dropCategoryLabel carries the localized catalog vocabulary; it returns the
  // raw category for a locale it has no word for, which then gets the same
  // capitalization as before.
  const named = dropCategoryLabel(category, lang);
  return named.charAt(0).toUpperCase() + named.slice(1);
}

// IntegrationCard is one tile in the index grid. `connected` shows a
// green status dot next to the name — the at-a-glance "this is set up"
// signal that pairs with the Connected section heading.
function IntegrationCard({
  slug,
  meta,
  drops,
  connected,
  ailing,
}: {
  slug: string;
  meta: { name: string; description: string; brand_logo?: string };
  drops: Manifest[];
  connected: boolean;
  // ailing: connected, but an account needs reconnecting. Amber, not green —
  // "set up" and "working" are different claims, and only the second one is
  // what someone scanning this page is actually asking.
  ailing: boolean;
}) {
  const { t, i18n } = useTranslation();
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
              className={
                "connection-dot integration-card-dot " + (ailing ? "ailing" : "on")
              }
              title={
                ailing
                  ? t("integrations.needsReconnectTip")
                  : t("integrations.connectedTip")
              }
            />
          )}
        </div>
        <p className="integration-card-desc">
          {integrationProse(`${slug}.description`, meta.description, i18n.language)}
        </p>
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
): { needsSetup: boolean; connected: boolean; ailing: boolean } {
  const reqs = dedupeRequirements(drops);
  const fields = drops.find((d) => d.connection_fields?.length)?.connection_fields ?? [];
  if (reqs.length === 0 && fields.length === 0) {
    return { needsSetup: false, connected: false, ailing: false };
  }
  const reqsOk = reqs.every((req) =>
    req.kind === "secret"
      ? (secrets ?? []).includes(req.name)
      : ((providers ?? []).find((p) => p.name === req.name)?.accounts.length ?? 0) > 0,
  );
  // Set up, but not working: an account whose grant is dead or whose scopes
  // fell behind. Green here would be a lie — this is the app the user's flow
  // is failing on, and the index is where they look first.
  const ailing = reqs.some((req) => {
    if (req.kind === "secret") return false;
    const p = (providers ?? []).find((x) => x.name === req.name);
    return ((p?.needs_reconnect?.length ?? 0) + (p?.stale_accounts?.length ?? 0)) > 0;
  });
  let fieldsOk = true;
  if (fields.length > 0) {
    const required = fields.filter((f) => f.required);
    const isSet = (f: ConnectionField) => (secrets ?? []).includes(`conn.${slug}.${f.key}`);
    fieldsOk = required.length > 0 ? required.every(isSet) : fields.some(isSet);
  }
  const connected = reqsOk && fieldsOk;
  return { needsSetup: !connected, connected, ailing: connected && ailing };
}

// AppDetail is /apps/:slug — the per-app "profile" page. Shows
// the hero (logo + name + full prose), the Connection card(s), and every
// drop the app ships with its ports and a collapsed params hint.
export function AppDetail() {
  const { t, i18n } = useTranslation();
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
    let cancelled = false;
    api
      .listDrops(token)
      .then((r) => {
        if (!cancelled) setDrops(r.drops);
      })
      .catch((e) => {
        if (cancelled) return;
        setError(explainApiError(e, t));
      });
    return () => {
      cancelled = true;
    };
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
        <ErrorNotice>{error}</ErrorNotice>
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
            <p className="integration-hero-desc">
              {integrationProse(
                `${slug}.description`,
                meta.description,
                i18n.language,
              )}
            </p>
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

      {/* Operator notes behind a disclosure — OAuth scopes, the daemon env vars
          and webhook paths a self-hoster has to set, API version pins, token
          rotation windows. Irrelevant to someone who just wants to connect an
          account, and unobtainable without reading the source if it isn't
          here, so it collapses by default rather than being cut. Same
          shut-by-default pattern as the per-drop "Wiring details". */}
      {meta.technical_notes && (
        <details className="integration-notes">
          <summary>{t("integrations.technicalNotes")}</summary>
          <p>
            {integrationProse(
              `${slug}.technical_notes`,
              meta.technical_notes,
              i18n.language,
            )}
          </p>
        </details>
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
  // The integration is testable when any of its drops reports a live
  // connection check (the daemon computes connection_verifiable).
  const connectionVerifiable = useMemo(
    () => drops.some((d) => d.connection_verifiable),
    [drops],
  );
  const needsSecret = reqs.some((r) => r.kind === "secret") || connectionFields.length > 0;
  const needsOAuth = reqs.some((r) => r.kind === "oauth");

  const [secrets, setSecrets] = useState<string[] | null>(null);
  const [secretsOff, setSecretsOff] = useState(false);
  // secretsErr/providersErr latch a non-501 fetch failure. Without them a
  // failed fetch left `secrets` null forever, so the card showed "Loading…"
  // indefinitely; now the card shows an error with a Retry instead.
  const [secretsErr, setSecretsErr] = useState(false);
  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(null);
  const [providersOff, setProvidersOff] = useState(false);
  const [providersErr, setProvidersErr] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // refresh re-fetches connection state. `isCancelled` lets the mount effect
  // discard a response that resolves after the user navigated away (stale
  // write); on-demand callers (after connect/disconnect) pass nothing.
  const refresh = (isCancelled?: () => boolean) => {
    if (!token) return;
    const live = () => !(isCancelled?.() ?? false);
    if (needsSecret) {
      setSecretsErr(false);
      api
        .listSecrets(token, undefined, undefined, true)
        .then((r) => {
          if (!live()) return;
          setSecrets(r.secrets);
          setSecretsOff(false);
        })
        .catch((e) => {
          if (!live()) return;
          if (e instanceof APIError && featureUnavailable(e.status)) setSecretsOff(true);
          else {
            setSecretsErr(true);
            setError(explainApiError(e, t));
          }
        });
    }
    if (needsOAuth) {
      setProvidersErr(false);
      api
        .listProviders(token)
        .then((r) => {
          if (!live()) return;
          setProviders(r.providers);
          setProvidersOff(false);
        })
        .catch((e) => {
          if (!live()) return;
          if (e instanceof APIError && featureUnavailable(e.status)) setProvidersOff(true);
          else {
            setProvidersErr(true);
            setError(explainApiError(e, t));
          }
        });
    }
  };
  // reqs is derived from drops (stable per detail load), so token is the
  // only real dependency; needsSecret/needsOAuth are read inside refresh.
  useEffect(() => {
    let cancelled = false;
    refresh(() => cancelled);
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, slug]);

  // OAuth bounces back to /apps/:slug?oauth=success|error. Show
  // the result once, then strip the params so a refresh doesn't re-show.
  // Snapshot the OAuth callback result on the first render, then strip the
  // params from the URL so a refresh (or the effects below re-running) can't
  // re-show a stale banner. The banner stays visible from this local state
  // until the user dismisses it.
  const [oauthBanner, setOauthBanner] = useState<{
    result: string;
    error: string;
    name: string;
  } | null>(() => {
    const result = searchParams.get("oauth");
    if (!result) return null;
    const provider = searchParams.get("provider") ?? "";
    return {
      result,
      error: searchParams.get("error") ?? "",
      name: provider ? oauthProviderDisplay(provider).name : name,
    };
  });
  useEffect(() => {
    if (!searchParams.get("oauth")) return;
    const next = new URLSearchParams(searchParams);
    next.delete("oauth");
    next.delete("provider");
    next.delete("account");
    next.delete("error");
    setSearchParams(next, { replace: true });
    // Run once for this callback round-trip; setSearchParams clears the
    // param so it won't re-fire.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const dismissBanner = () => setOauthBanner(null);

  if (reqs.length === 0 && connectionFields.length === 0) return null;

  return (
    <section className="integration-connections">
      {oauthBanner?.result === "success" && (
        <div className="card connections-banner success">
          <span>
            {t("integrations.connection.connectSuccess", {
              name: oauthBanner.name,
            })}
          </span>
          <Button variant="link" onClick={dismissBanner}>
            {t("common.dismiss")}
          </Button>
        </div>
      )}
      {oauthBanner?.result === "error" && (
        <ErrorNotice
          className="connections-banner"
          action={
            <Button variant="link" onClick={dismissBanner}>
              {t("common.dismiss")}
            </Button>
          }
        >
          {t("integrations.connection.connectFailed", {
            error: oauthBanner.error || t("connections.unknownError"),
          })}
        </ErrorNotice>
      )}
      {error && <ErrorNotice>{error}</ErrorNotice>}
      {connectionFields.length > 0 && (
        <ConnectionFieldsCard
          fields={connectionFields}
          name={name}
          slug={slug}
          secrets={secrets}
          loading={secrets === null && !secretsOff && !secretsErr}
          off={secretsOff}
          errored={secretsErr}
          onRetry={() => refresh()}
          canWrite={canWrite}
          verifiable={connectionVerifiable}
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
            loading={secrets === null && !secretsOff && !secretsErr}
            off={secretsOff}
            errored={secretsErr}
            onRetry={() => refresh()}
            canWrite={canWrite}
            onChanged={refresh}
          />
        ) : (
          <OAuthCard
            key={`oauth:${req.name}`}
            req={req}
            status={providers?.find((p) => p.name === req.name) ?? null}
            loading={providers === null && !providersOff && !providersErr}
            off={providersOff}
            errored={providersErr}
            onRetry={() => refresh()}
            canWrite={canWrite}
            slug={slug}
            integration={name}
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
  errored,
  onRetry,
  canWrite,
  onChanged,
}: {
  req: ConnectionRequirement;
  name: string;
  configured: boolean;
  loading: boolean;
  off: boolean;
  errored?: boolean;
  onRetry?: () => void;
  canWrite: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [value, setValue] = useState("");
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);

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
      setErr(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!token) return;
    setRemoving(true);
    setErr(null);
    try {
      await api.deleteSecret(token, req.name);
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setRemoving(false);
      // Always re-read from the server so the card reflects the real
      // state whether the delete succeeded or failed.
      onChanged();
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
      ) : errored ? (
        <div className="connection-note">
          <p>{t("integrations.connection.loadFailed")}</p>
          {onRetry && (
            <Button variant="ghost" onClick={onRetry}>
              {t("common.retry")}
            </Button>
          )}
        </div>
      ) : loading ? (
        <p className="connection-note">{t("common.loading")}</p>
      ) : configured && !editing ? (
        <>
          <label className="connection-field">
            <span className="connection-field-label">{fieldLabel}</span>
            <input type="password" value="••••••••••" readOnly aria-label={fieldLabel} />
          </label>
          {err && <ErrorNotice>{err}</ErrorNotice>}
          {canWrite && (
            <div className="connection-card-footer">
              <Button variant="ghost" onClick={() => setEditing(true)}>
                {t("integrations.connection.edit")}
              </Button>
              <Button
                variant="danger"
                onClick={() => setConfirming(true)}
                disabled={removing}
              >
                {removing
                  ? t("integrations.connection.disconnecting")
                  : t("integrations.connection.disconnect")}
              </Button>
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
          {err && <ErrorNotice>{err}</ErrorNotice>}
          <div className="connection-card-footer">
            <Button type="submit" variant="primary" disabled={busy || !value}>
              {busy ? t("connections.saving") : t("connections.connect")}
            </Button>
            {configured && (
              <Button
                variant="ghost"
                onClick={() => {
                  setEditing(false);
                  setValue("");
                }}
              >
                {t("common.cancel")}
              </Button>
            )}
          </div>
        </form>
      ) : (
        <p className="connection-note">{t("integrations.connection.notConfigured")}</p>
      )}
      {confirming && (
        <ConfirmModal
          title={t("integrations.connection.disconnect")}
          message={t("integrations.connection.removeConfirm", { name })}
          confirmLabel={t("integrations.connection.disconnect")}
          danger
          onConfirm={() => {
            setConfirming(false);
            void remove();
          }}
          onCancel={() => setConfirming(false)}
        />
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
  errored,
  onRetry,
  canWrite,
  slug,
  integration,
}: {
  req: ConnectionRequirement;
  status: OAuthProviderStatus | null;
  loading: boolean;
  off: boolean;
  errored?: boolean;
  onRetry?: () => void;
  canWrite: boolean;
  slug: string;
  integration: string;
}) {
  const { t } = useTranslation();
  const meta = oauthProviderDisplay(req.name);
  const accounts = status?.accounts ?? [];
  const connected = accounts.length > 0;
  // An account whose grant is dead (the refresh was rejected) or whose
  // scopes no longer cover what we ask for. Both mean the same thing to the
  // person reading the page: this one needs reconnecting.
  const broken = new Set([
    ...(status?.needs_reconnect ?? []),
    ...(status?.stale_accounts ?? []),
  ]);
  // A run that failed on this app links here with ?reconnect=<account>, so
  // the account that actually broke is the one called out.
  const flagged = new URLSearchParams(window.location.search).get("reconnect");
  const healthy = connected && broken.size === 0;

  // account undefined = connect a new one; a name re-authorizes that account
  // in place rather than adding a second.
  const connect = (account?: string) => {
    // Pass the integration so the consent screen requests only this
    // service's scopes (incremental authorization) — e.g. connecting from
    // the Google Sheets page won't ask for Gmail/Forms.
    window.location.assign(
      api.oauthAuthorizeUrl(req.name, `/apps/${encodeURIComponent(slug)}`, account, integration),
    );
  };

  return (
    <div className="connection-card">
      <ConnectionStatus
        connected={healthy}
        title={
          !connected
            ? t("integrations.connection.connectPrompt", { name: meta.name })
            : healthy
            ? t("integrations.connection.connectedTo", { name: meta.name })
            : t("integrations.connection.needsReconnect", { name: meta.name })
        }
      />
      {/* The accounts themselves, each with its own state and its own fix.
          Without this the card could only say "connected", so a dead grant
          looked healthy and the only button on offer added a SECOND account
          instead of repairing the broken one. */}
      {connected && (
        <ul className="connection-accounts">
          {accounts.map((acct) => {
            const needsFixing = broken.has(acct);
            return (
              <li
                key={acct}
                className={
                  "connection-account" +
                  (needsFixing ? " needs-reconnect" : "") +
                  (acct === flagged ? " flagged" : "")
                }
              >
                <span className={"connection-dot " + (needsFixing ? "off" : "on")} />
                <span className="connection-account-name">{acct}</span>
                {needsFixing && (
                  <span className="connection-account-note">
                    {t("integrations.connection.accountNeedsReconnect")}
                  </span>
                )}
                {canWrite && (
                  <Button
                    variant={needsFixing ? "primary" : "ghost"}
                    onClick={() => connect(acct)}
                  >
                    {t("connections.reconnect")}
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {off ? (
        <p className="connection-note">{t("integrations.connection.oauthOff")}</p>
      ) : errored ? (
        <div className="connection-note">
          <p>{t("integrations.connection.loadFailed")}</p>
          {onRetry && (
            <Button variant="ghost" onClick={onRetry}>
              {t("common.retry")}
            </Button>
          )}
        </div>
      ) : loading ? (
        <p className="connection-note">{t("common.loading")}</p>
      ) : canWrite ? (
        <div className="connection-card-footer">
          <Button variant={connected ? "ghost" : "primary"} onClick={() => connect()}>
            {connected ? t("connections.connectAnother") : t("connections.connect")}
          </Button>
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
  errored,
  onRetry,
  canWrite,
  verifiable,
  onChanged,
}: {
  fields: ConnectionField[];
  name: string;
  slug: string;
  secrets: string[] | null;
  loading: boolean;
  off: boolean;
  errored?: boolean;
  onRetry?: () => void;
  canWrite: boolean;
  verifiable: boolean;
  onChanged: () => void;
}) {
  const { t, i18n } = useTranslation();
  const { token } = useAuth();
  const [values, setValues] = useState<Record<string, string>>({});
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  // testState tracks the "Test connection" button: idle, in-flight, or a
  // resolved result with a human message. Separate from `err` (the save
  // form's error) so testing an existing connection doesn't disturb the form.
  const [testState, setTestState] = useState<
    { kind: "idle" } | { kind: "testing" } | { kind: "ok" } | { kind: "fail"; message: string }
  >({ kind: "idle" });

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
    setTestState({ kind: "idle" });
    try {
      // Verify-before-save: the daemon tests the credentials (when the
      // integration supports it) and stores them only if they work, so a bad
      // value is rejected here with the real reason instead of being saved
      // and shown as "Connected".
      const entered: Record<string, string> = {};
      for (const f of pending) entered[f.key] = values[f.key];
      await api.connectIntegration(token, slug, entered);
      setValues({});
      setEditing(false);
      onChanged();
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const test = async () => {
    if (!token) return;
    setTestState({ kind: "testing" });
    try {
      const r = await api.verifyIntegration(token, slug);
      setTestState(r.ok ? { kind: "ok" } : { kind: "fail", message: r.error ?? "" });
    } catch (e) {
      setTestState({ kind: "fail", message: explainApiError(e, t) });
    }
  };

  const disconnect = async () => {
    if (!token) return;
    setRemoving(true);
    setErr(null);
    try {
      for (const f of fields) {
        if (isSet(f)) await api.deleteSecret(token, keyFor(f));
      }
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setRemoving(false);
      // Re-read from the server regardless: a partial failure (some fields
      // deleted, one errored) still needs the card to show the true state.
      onChanged();
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
      ) : errored ? (
        <div className="connection-note">
          <p>{t("integrations.connection.loadFailed")}</p>
          {onRetry && (
            <Button variant="ghost" onClick={onRetry}>
              {t("common.retry")}
            </Button>
          )}
        </div>
      ) : loading ? (
        <p className="connection-note">{t("common.loading")}</p>
      ) : connected && !editing ? (
        <>
          <ul className="connection-fields-summary">
            {fields.map((f) => (
              <li key={f.key}>
                <span className="connection-field-label">
                  {connectionText(f.label, i18n.language)}
                </span>
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
          {err && <ErrorNotice>{err}</ErrorNotice>}
          {verifiable && testState.kind === "ok" && (
            <p className="connection-test-result ok">{t("integrations.connection.testOk")}</p>
          )}
          {verifiable && testState.kind === "fail" && (
            <p className="connection-test-result fail">
              {t("integrations.connection.testFailed", {
                error: testState.message || t("connections.unknownError"),
              })}
            </p>
          )}
          {canWrite && (
            <div className="connection-card-footer">
              {verifiable && (
                <Button
                  variant="ghost"
                  onClick={() => void test()}
                  disabled={testState.kind === "testing"}
                >
                  {testState.kind === "testing"
                    ? t("integrations.connection.testing")
                    : t("integrations.connection.test")}
                </Button>
              )}
              <Button variant="ghost" onClick={() => setEditing(true)}>
                {t("integrations.connection.edit")}
              </Button>
              <Button
                variant="danger"
                onClick={() => setConfirming(true)}
                disabled={removing}
              >
                {removing
                  ? t("integrations.connection.disconnecting")
                  : t("integrations.connection.disconnect")}
              </Button>
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
                {connectionText(f.label, i18n.language)}
                {isSet(f) && (
                  <span className="connection-field-set-hint"> · {t("integrations.connection.fieldSet")}</span>
                )}
              </span>
              {f.options?.length ? (
                // Enum field → dropdown. The blank option means "leave at the
                // default" (the drop's own fallback, e.g. Nominatim).
                <select
                  value={values[f.key] ?? ""}
                  onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
                >
                  <option value="">{f.placeholder || t("connections.defaultOption")}</option>
                  {f.options.map((opt) => (
                    <option key={opt} value={opt}>
                      {opt}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  type={f.secret ? "password" : "text"}
                  placeholder={f.placeholder ?? ""}
                  value={values[f.key] ?? ""}
                  onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
                  autoComplete="off"
                />
              )}
              {/* Where to GET the value. Under the input, not inside it, so it
                  survives the first keystroke — this is the moment the user is
                  off in another product's settings hunting for a token. */}
              {f.help && (
                <span className="connection-field-help">
                  {connectionText(f.help, i18n.language)}
                </span>
              )}
            </label>
          ))}
          {err && <ErrorNotice>{err}</ErrorNotice>}
          <div className="connection-card-footer">
            <Button type="submit" variant="primary" disabled={busy}>
              {busy
                ? verifiable
                  ? t("integrations.connection.verifying")
                  : t("connections.saving")
                : t("connections.connect")}
            </Button>
            {connected && (
              <Button
                variant="ghost"
                onClick={() => {
                  setEditing(false);
                  setValues({});
                }}
              >
                {t("common.cancel")}
              </Button>
            )}
          </div>
        </form>
      ) : (
        <p className="connection-note">{t("integrations.connection.notConfigured")}</p>
      )}
      {confirming && (
        <ConfirmModal
          title={t("integrations.connection.disconnect")}
          message={t("integrations.connection.disconnectFieldsConfirm", { name })}
          confirmLabel={t("integrations.connection.disconnect")}
          danger
          onConfirm={() => {
            setConfirming(false);
            void disconnect();
          }}
          onCancel={() => setConfirming(false)}
        />
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

// DropCard renders one drop's "help" entry: icon + label + module ID, full
// description, and its input + output ports behind a disclosure.
//
// It used to end with the drop's params_schema, pretty-printed as a raw JSON
// dump. That is gone. The schema's human-readable half — every field title,
// help line and dropdown option — is what the Inspector's form renders, and
// dropFields.sv.ts translates 870 of those strings for it; the dump showed the
// untranslated original, so a Swedish user opening this disclosure met English
// JSON describing fields the editor shows them in Swedish. The machine-readable
// half is already served to the consumers that want it, by
// GET /api/v1/catalog/drops/{id} (which the MCP describe_drop tool proxies) and
// over gRPC. That left a surface with no audience: 122 schemas, ~4,200 lines of
// JSON, for a page whose job is "what does this app do".
function DropCard({ drop }: { drop: Manifest }) {
  const { t, i18n } = useTranslation();
  const Icon = iconFor(drop.icon, drop.category);
  const branded = isBrandedIcon(drop.icon);
  const color = dropColor(drop.category, drop.color);
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
          <h3>
            {dropLabel(drop, i18n.language)}
            {drop.subtitle ? (
              <span className="drop-card-action">
                {" · " + dropSubtitle(drop, i18n.language)}
              </span>
            ) : null}
          </h3>
          <code className="drop-card-id">{drop.id}</code>
        </div>
      </div>
      {drop.description && (
        <p className="drop-card-desc">{dropDescription(drop, i18n.language)}</p>
      )}
      {/* Wiring lives behind one disclosure so the visible-by-default
          surface is "what this step is for"; one click expands to "what
          connects to it." Keeps non-technical scanners focused without
          hiding the port names from someone planning a flow. */}
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
                        <span className="port-label">
                          {" — " + portLabel(p.label, i18n.language)}
                        </span>
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
                        <span className="port-label">
                          {" — " + portLabel(p.label, i18n.language)}
                        </span>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        </details>
      )}
    </div>
  );
}

// hasWiringDetails reports whether a drop has ports worth revealing. A drop
// with neither inputs nor outputs would render an empty disclosure — skip the
// summary entirely in that case so the card stays clean.
//
// This used to count `params_schema` as a reason to open the disclosure too.
// Dropping it costs no drop its details section: every drop in the catalogue
// that declares a params schema also declares ports, so the condition is
// unchanged in practice.
function hasWiringDetails(d: Manifest): boolean {
  return (
    (d.inputs && d.inputs.length > 0) ||
    (d.outputs && d.outputs.length > 0)
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
