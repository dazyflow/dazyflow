// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Box, Search } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { useAuth } from "../auth";
import { DropIcon, ICON, iconFor, isBrandedIcon } from "../icons";
import {
  connectionText,
  integrationName,
  integrationProse,
  splitConnectionNote,
  dropCategoryLabel,
  dropDescription,
  dropLabel,
  dropSubtitle,
  portLabel,
} from "../lib/dropText";
import { Button } from "../components/ui/Button";
import { ConfirmModal } from "../components/ui/ConfirmModal";
import {
  integrationMeta,
  integrationNameFromSlug,
  integrationSlug,
  oauthProviderDisplay,
} from "../integrationMeta";
import { ErrorNotice } from "../components/ui/ErrorNotice";
import { EmptyState } from "../components/ui/EmptyState";
import type {
  ConnectionField,
  ConnectionRequirement,
  Manifest,
  OAuthProviderStatus,
} from "../types";
import { featureUnavailable } from "../lib/explainApiError";
import { Loading } from "../components/ui/Loading";

// One card per integration the daemon knows about.
export function Apps() {
  const { t, i18n } = useTranslation();
  const { token } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const [drops, setDrops] = useState<Manifest[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [secrets, setSecrets] = useState<string[] | null>(null);
  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(null);

  useEffect(() => {
    if (!token) return;
    // A late response from a previous token must not overwrite the current one.
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

  // Grouped by integration slug, which is what a connection is keyed on.
  const groups = useMemo(() => {
    const nameOf = (g: { slug: string; meta: { name: string } }) =>
      groupDisplayName(g.slug, g.meta.name, t, i18n.language);
    const rank = (slug: string) => {
      const i = FEATURED_APPS.indexOf(slug);
      return i === -1 ? FEATURED_APPS.length : i;
    };
    return buildGroups(drops ?? []).sort((a, b) => {
      const d = rank(a.slug) - rank(b.slug);
      return d !== 0
        ? d
        : nameOf(a).localeCompare(nameOf(b), undefined, { sensitivity: "base" });
    });
  }, [drops, t, i18n.language]);

  // Computed once for the whole list, not per card.
  const states = useMemo(() => {
    const out = new Map<string, ReturnType<typeof appConnectionState>>();
    for (const g of groups) {
      out.set(g.slug, appConnectionState(g.slug, g.drops, secrets, providers));
    }
    return out;
  }, [groups, secrets, providers]);

  const haystacks = useMemo(() => {
    const out = new Map<string, string>();
    for (const g of groups) {
      const name = groupDisplayName(g.slug, g.meta.name, t, i18n.language);
      const parts = [name, g.slug, g.meta.description];
      for (const d of g.drops) {
        parts.push(dropLabel(d, i18n.language), d.id, dropSubtitle(d, i18n.language) ?? "");
      }
      out.set(g.slug, foldForSearch(parts.join(" ")));
    }
    return out;
  }, [groups, t, i18n.language]);

  const query = searchParams.get("q") ?? "";
  const statusFilter = (searchParams.get("status") ?? "all") as AppStatusFilter;
  const categoryFilter = searchParams.get("category") ?? "";
  const page = Math.max(1, Number(searchParams.get("page") ?? "1") || 1);

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(searchParams);
    if (value) next.set(key, value);
    else next.delete(key);
    if (key !== "page") next.delete("page");
    setSearchParams(next, { replace: true });
  };
  const clearFilters = () => {
    const next = new URLSearchParams(searchParams);
    for (const key of ["q", "status", "category", "page"]) next.delete(key);
    setSearchParams(next, { replace: true });
  };

  const categories = useMemo(() => {
    const seen = new Set<string>();
    for (const g of groups) for (const d of g.drops) if (d.category) seen.add(d.category);
    return [...seen]
      .map((c) => ({ value: c, label: categoryLabel(c, t, i18n.language) }))
      .sort((a, b) => a.label.localeCompare(b.label, undefined, { sensitivity: "base" }));
  }, [groups, t, i18n.language]);

  const filtered = useMemo(() => {
    const needle = foldForSearch(query);
    return groups.filter((g) => {
      if (needle && !(haystacks.get(g.slug) ?? "").includes(needle)) return false;
      if (categoryFilter && !g.drops.some((d) => d.category === categoryFilter)) return false;
      const st = states.get(g.slug);
      switch (statusFilter) {
        case "connected":
          return !!st?.connected;
        case "needs_setup":
          return !!st?.needsSetup;
        case "ailing":
          return !!st?.ailing;
        default:
          return true;
      }
    });
  }, [groups, haystacks, states, query, categoryFilter, statusFilter]);

  const pageCount = Math.max(1, Math.ceil(filtered.length / APPS_PER_PAGE));
  const current = Math.min(page, pageCount);
  const from = (current - 1) * APPS_PER_PAGE;
  const shown = filtered.slice(from, from + APPS_PER_PAGE);
  const filtersActive = !!(query || categoryFilter || statusFilter !== "all");

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
        <Loading />
      </div>
    );
  }

  return (
    <div className="page">
      <h1>{t("integrations.title")}</h1>
      <p className="page-sub">{t("integrations.intro")}</p>

      <div className="flow-toolbar">
        <div className="search-box flow-search">
          <Search size={ICON.sm} aria-hidden />
          <input
            type="search"
            value={query}
            onChange={(e) => setParam("q", e.target.value)}
            placeholder={t("integrations.searchPlaceholder")}
            aria-label={t("integrations.searchPlaceholder")}
          />
        </div>
        <label className="flow-filter">
          <span>{t("common.status")}</span>
          <select value={statusFilter} onChange={(e) => setParam("status", e.target.value)}>
            <option value="all">{t("integrations.statusAll")}</option>
            <option value="connected">{t("integrations.connectedTip")}</option>
            <option value="needs_setup">{t("integrations.needsSetupHead")}</option>
            <option value="ailing">{t("integrations.statusAiling")}</option>
          </select>
        </label>
        <label className="flow-filter">
          <span>{t("integrations.categoryFilter")}</span>
          <select value={categoryFilter} onChange={(e) => setParam("category", e.target.value)}>
            <option value="">{t("integrations.categoryAll")}</option>
            {categories.map((c) => (
              <option key={c.value} value={c.value}>
                {c.label}
              </option>
            ))}
          </select>
        </label>
        {filtersActive && (
          <Button className="integrations-filter-clear" onClick={clearFilters}>
            {t("integrations.filterClear")}
          </Button>
        )}
      </div>

      {/* The count is what makes a filter honest: it says how much was matched,
          and with pagination it says which slice is on screen. role=status so a
          screen reader hears the new total after a keystroke, rather than the
          grid silently changing under it. */}
      <div className="integrations-count muted" role="status">
        {filtered.length === 0
          ? t("integrations.countNone")
          : t("integrations.countRange", {
              from: from + 1,
              to: from + shown.length,
              total: filtered.length,
            })}
      </div>

      {filtered.length === 0 ? (
        <EmptyState
          icon={Box}
          title={t("integrations.noResultsTitle")}
          action={
            filtersActive ? (
              <Button onClick={clearFilters}>{t("integrations.filterClear")}</Button>
            ) : undefined
          }
        >
          {t("integrations.noResultsBody")}
        </EmptyState>
      ) : (
        <>
          <div className="integration-grid">
            {shown.map((g) => (
              <IntegrationCard
                key={g.slug}
                {...g}
                connected={!!states.get(g.slug)?.connected}
                ailing={!!states.get(g.slug)?.ailing}
                needsSetup={!!states.get(g.slug)?.needsSetup}
              />
            ))}
          </div>
          <Pager
            page={current}
            pageCount={pageCount}
            onPage={(n) => setParam("page", String(n))}
          />
        </>
      )}
    </div>
  );
}

const APPS_PER_PAGE = 24;

type AppStatusFilter = "all" | "connected" | "needs_setup" | "ailing";

function foldForSearch(s: string): string {
  return s
    .normalize("NFD")
    .replace(/\p{Diacritic}/gu, "")
    .toLowerCase()
    .trim();
}

// Local on purpose: the first of its kind, so it is not yet a shared primitive.
function Pager({
  page,
  pageCount,
  onPage,
}: {
  page: number;
  pageCount: number;
  onPage: (n: number) => void;
}) {
  const { t } = useTranslation();
  if (pageCount <= 1) return null;
  const pages: Array<number | "gap"> = [];
  const window = 1; // pages either side of the current one
  let last = 0;
  for (let n = 1; n <= pageCount; n++) {
    const near = Math.abs(n - page) <= window;
    if (n === 1 || n === pageCount || near) {
      if (last && n - last > 1) pages.push("gap");
      pages.push(n);
      last = n;
    }
  }
  return (
    <nav className="integrations-pager" aria-label={t("integrations.pagerLabel")}>
      <Button disabled={page <= 1} onClick={() => onPage(page - 1)}>
        {t("integrations.pagePrev")}
      </Button>
      {pages.map((p, i) =>
        p === "gap" ? (
          <span key={`gap-${i}`} className="integrations-pager-gap" aria-hidden>
            …
          </span>
        ) : (
          <Button
            key={p}
            className={p === page ? "integrations-pager-current" : undefined}
            aria-current={p === page ? "page" : undefined}
            aria-label={t("integrations.pageN", { n: p })}
            onClick={() => onPage(p)}
          >
            {p}
          </Button>
        ),
      )}
      <Button disabled={page >= pageCount} onClick={() => onPage(page + 1)}>
        {t("integrations.pageNext")}
      </Button>
    </nav>
  );
}

function categoryLabel(
  category: string,
  t: (k: string) => string,
  lang?: string,
): string {
  if (category === "ai") return t("integrations.categoryAi");
  const named = dropCategoryLabel(category, lang);
  return named.charAt(0).toUpperCase() + named.slice(1);
}

function IntegrationCard({
  slug,
  meta,
  drops,
  connected,
  ailing,
  needsSetup,
}: {
  slug: string;
  meta: { name: string; description: string; brand_logo?: string };
  drops: Manifest[];
  connected: boolean;
  ailing: boolean;
  needsSetup: boolean;
}) {
  const { t, i18n } = useTranslation();
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
            {groupDisplayName(slug, meta.name, t, i18n.language)}
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
        <div className="integration-card-foot muted">
          <span>{t("integrations.stepCount", { count: drops.length })}</span>
          {/* Only the states worth acting on are named. "Ready to use" on an app
              that needs no setup is a label on every second card, saying nothing
              — the absence of a warning is the message there. */}
          {ailing ? (
            <span className="integration-card-state ailing">
              {t("integrations.statusAiling")}
            </span>
          ) : connected ? (
            <span className="integration-card-state on">{t("integrations.connectedTip")}</span>
          ) : needsSetup ? (
            <span className="integration-card-state">{t("integrations.needsSetupHead")}</span>
          ) : null}
        </div>
      </div>
    </Link>
  );
}

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
  // Set up but not working: a dead grant, or scopes that no longer cover the drop.
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

export function AppDetail() {
  const { t, i18n } = useTranslation();
  const slugRaw = window.location.pathname.split("/").pop() ?? "";
  const slug = decodeURIComponent(slugRaw);
  const { token, hasPerm } = useAuth();
  // Connecting writes a credential, so it is gated on secret:write.
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
    const base = integrationMeta[slug] ?? uncuratedMeta(slug, filtered);
    const m = { ...base, name: groupDisplayName(slug, base.name, t, i18n.language) };
    return { meta: m, integrationDrops: filtered };
  }, [drops, slug, t, i18n.language]);

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
        <Loading />
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
        <div className="card" style={{ marginTop: "var(--space-3)" }}>
          {t("integrations.noDrops")}
        </div>
      </div>
    );
  }

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
  const connectionFields = useMemo(
    () => drops.find((d) => d.connection_fields?.length)?.connection_fields ?? [],
    [drops],
  );
  const connectionVerifiable = useMemo(
    () => drops.some((d) => d.connection_verifiable),
    [drops],
  );
  const needsSecret = reqs.some((r) => r.kind === "secret") || connectionFields.length > 0;
  const needsOAuth = reqs.some((r) => r.kind === "oauth");
  const manyCards = (connectionFields.length > 0 ? 1 : 0) + reqs.length > 1;

  const [secrets, setSecrets] = useState<string[] | null>(null);
  const [secretsOff, setSecretsOff] = useState(false);
  const [secretsErr, setSecretsErr] = useState(false);
  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(null);
  const [providersOff, setProvidersOff] = useState(false);
  const [providersErr, setProvidersErr] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // isCancelled lets the mount effect drop a response that arrives after unmount.
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
  useEffect(() => {
    let cancelled = false;
    refresh(() => cancelled);
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, slug]);

  // OAuth bounces back with a query flag; it must be cleared after reading.
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
          qualify={manyCards}
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
            qualify={manyCards}
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
            unavailable={
              providers !== null &&
              !providersOff &&
              !providersErr &&
              !providers.some((p) => p.name === req.name)
            }
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

function SecretCard({
  req,
  name,
  qualify,
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
  // Some apps ship several credentials, so the title has to name which.
  qualify?: boolean;
  configured: boolean;
  loading: boolean;
  off: boolean;
  errored?: boolean;
  onRetry?: () => void;
  canWrite: boolean;
  onChanged: () => void;
}) {
  const { t, i18n } = useTranslation();
  const { token } = useAuth();
  const [value, setValue] = useState("");
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);

  const note = req.note ?? req.name;
  const parts = splitConnectionNote(note);
  const fieldLabel = connectionText(parts.label, i18n.language);
  const placeholder = parts.example || t("integrations.connection.valuePlaceholder");
  const titleName = qualify ? `${name} — ${fieldLabel}` : name;

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
      onChanged();
    }
  };

  return (
    <div className="connection-card">
      <ConnectionStatus
        connected={configured}
        title={
          configured
            ? t("integrations.connection.connectedTo", { name: titleName })
            : t("integrations.connection.connectPrompt", { name: titleName })
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
                {t("common.edit")}
              </Button>
              <Button
                variant="danger"
                onClick={() => setConfirming(true)}
                disabled={removing}
              >
                {removing
                  ? t("integrations.connection.disconnecting")
                  : t("common.disconnect")}
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
              {busy ? t("common.saving") : t("common.connect")}
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
          title={t("common.disconnect")}
          message={t("integrations.connection.removeConfirm", { name })}
          confirmLabel={t("common.disconnect")}
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

function OAuthCard({
  req,
  status,
  loading,
  off,
  errored,
  unavailable,
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
  unavailable?: boolean;
  onRetry?: () => void;
  canWrite: boolean;
  slug: string;
  integration: string;
}) {
  const { t } = useTranslation();
  const meta = oauthProviderDisplay(req.name);
  const accounts = status?.accounts ?? [];
  const connected = accounts.length > 0;
  const broken = new Set([
    ...(status?.needs_reconnect ?? []),
    ...(status?.stale_accounts ?? []),
  ]);
  const flagged = new URLSearchParams(window.location.search).get("reconnect");
  const healthy = connected && broken.size === 0;

  const connect = (account?: string) => {
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
      ) : unavailable && !connected ? (
        <p className="connection-note">
          {t("integrations.connection.providerUnavailable", {
            name: meta.name,
          })}
        </p>
      ) : canWrite ? (
        <div className="connection-card-footer">
          <Button variant={connected ? "ghost" : "primary"} onClick={() => connect()}>
            {connected ? t("connections.connectAnother") : t("common.connect")}
          </Button>
        </div>
      ) : !connected ? (
        <p className="connection-note">{t("integrations.connection.notConfigured")}</p>
      ) : null}
    </div>
  );
}

function ConnectionFieldsCard({
  fields,
  name,
  qualify,
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
  qualify?: boolean;
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
      onChanged();
    }
  };

  const showForm = canWrite && (!connected || editing);

  const identifying = fields.filter((f) => f.required);
  const titleName =
    qualify && identifying.length === 1 && identifying[0].label
      ? `${name} — ${connectionText(identifying[0].label, i18n.language)}`
      : name;

  return (
    <div className="connection-card">
      <ConnectionStatus
        connected={connected}
        title={
          connected
            ? t("integrations.connection.connectedTo", { name: titleName })
            : t("integrations.connection.connectPrompt", { name: titleName })
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
                {t("common.edit")}
              </Button>
              <Button
                variant="danger"
                onClick={() => setConfirming(true)}
                disabled={removing}
              >
                {removing
                  ? t("integrations.connection.disconnecting")
                  : t("common.disconnect")}
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
                <select
                  value={values[f.key] ?? ""}
                  onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
                >
                  <option value="">
                    {f.placeholder
                      ? connectionText(f.placeholder, i18n.language)
                      : t("connections.defaultOption")}
                  </option>
                  {f.options.map((opt) => (
                    <option key={opt} value={opt}>
                      {opt}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  type={f.secret ? "password" : "text"}
                  placeholder={connectionText(f.placeholder ?? "", i18n.language)}
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
                  : t("common.saving")
                : t("common.connect")}
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
          title={t("common.disconnect")}
          message={t("integrations.connection.disconnectFieldsConfirm", { name })}
          confirmLabel={t("common.disconnect")}
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

function DropCard({ drop }: { drop: Manifest }) {
  const { t, i18n } = useTranslation();
  return (
    <div className="drop-card">
      <div className="drop-card-head">
        <DropIcon
          icon={drop.icon}
          category={drop.category}
          brandColor={drop.color}
          brandLogo={drop.brand_logo}
          glyphSize={ICON.md}
        />
        <div className="drop-card-title">
          <h3>
            {dropLabel(drop, i18n.language)}
            {drop.subtitle ? (
              <span className="drop-card-action">
                {" · " + dropSubtitle(drop, i18n.language)}
              </span>
            ) : null}
          </h3>
        </div>
      </div>
      {drop.description && (
        <p className="drop-card-desc">{dropDescription(drop, i18n.language)}</p>
      )}
      {/* Wiring lives behind one disclosure so the visible-by-default
          surface is "what this step is for"; one click expands to "what
          connects to it." Keeps non-technical scanners focused without
          hiding the port names from someone planning a flow.
          The step's own id (gmail_get_attachments, …) lives in here too. It
          used to sit under the title on every card, so a page written for
          someone deciding whether an app does what they need opened with a
          column of snake_case identifiers. It is still one click away for the
          integrator who wants to grep for it, and it means this disclosure is
          never empty — every step has an id, ports or not. */}
      <details className="drop-card-wiring">
          <summary>{t("integrations.wiringDetails")}</summary>
          <div className="drop-card-ports">
            <div>
              <div className="drop-card-port-head">{t("integrations.stepId")}</div>
              <code className="drop-card-id">{drop.id}</code>
            </div>
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
    </div>
  );
}

function uncuratedMeta(
  slug: string,
  drops: Manifest[],
): { name: string; description: string } {
  return {
    name: drops.find((d) => d.integration)?.integration ?? integrationNameFromSlug(slug),
    description:
      drops.find((d) => d.integration_description)?.integration_description ?? "",
  };
}

function groupDisplayName(
  slug: string,
  metaName: string,
  t: (key: string) => string,
  lang?: string,
): string {
  if (slug === "standard-library") return t("integrations.builtinGroup");
  if (slug === "mcp") return t("integrations.mcpGroup");
  return integrationName(metaName, lang);
}

function integrationSlugFor(m: Manifest): string {
  if (m.integration && m.integration.trim() !== "") {
    return integrationSlug(m.integration);
  }
  return "standard-library";
}

const FEATURED_APPS = [
  "gmail",
  "google-sheets",
  "slack",
  "excel",
  "google-calendar",
  "google-drive",
  "notion",
  "stripe",
  "claude",
  "collections",
  "standard-library",
];

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
      meta: uncuratedMeta(slug, bySlug.get(slug)!),
      drops: bySlug.get(slug)!,
    });
  }
  return out;
}
