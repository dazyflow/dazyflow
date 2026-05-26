import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Box } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { iconFor, isBrandedIcon } from "../icons";
import {
  integrationMeta,
  integrationNameFromSlug,
  integrationSlug,
} from "../integrationMeta";
import type { Manifest } from "../types";

// Integrations is the index page — one card per integration the
// daemon knows about, derived from the live manifest registry plus
// curated prose from integrationMeta. Drops without an Integration
// field land in a "Standard library" bucket so the page covers
// everything the catalog shows in the editor.
//
// Card-level data:
//   - brand logo (from any drop's brand_logo, or curated override)
//   - integration name (curated, falls back to slug)
//   - short description (curated; truncated; full prose on detail)
//   - drop count
export function Integrations() {
  const { t } = useTranslation();
  const { token } = useAuth();
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

  // Group drops by integration slug. The standard-library bucket
  // catches anything without an Integration field — matches the
  // NodeCatalog grouping rules.
  const groups = useMemo(() => buildGroups(drops ?? []), [drops]);

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
      <div className="integration-grid">
        {groups.map(({ slug, meta, drops }) => {
          // Logo fallback chain: curated override → any drop's
          // brand_logo → category-derived lucide glyph from the
          // first drop. Drops carrying their own brand_logo means
          // we render the right vendor mark even for integrations
          // without a curated metadata entry (excel, mysql,
          // postgres, sqlite all ship per-drop logos).
          const brandLogo =
            meta.brand_logo ?? drops.find((d) => d.brand_logo)?.brand_logo;
          const headerDrop = drops[0];
          const HeaderIcon = headerDrop
            ? iconFor(headerDrop.icon, headerDrop.category)
            : Box;
          const headerBranded = isBrandedIcon(headerDrop?.icon);
          return (
            <Link
              key={slug}
              to={`/integrations/${encodeURIComponent(slug)}`}
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
                      <HeaderIcon
                        size={headerBranded ? 22 : 18}
                        strokeWidth={2.2}
                      />
                    </span>
                  )}
                  <h2>{meta.name}</h2>
                </div>
                <p className="integration-card-desc">
                  {truncate(meta.description, 160)}
                </p>
                <div className="integration-card-meta">
                  <span className="integration-card-count">
                    {t("integrations.drop", { count: drops.length })}
                  </span>
                </div>
              </div>
            </Link>
          );
        })}
      </div>
    </div>
  );
}

// IntegrationDetail is /integrations/:slug — the per-integration
// "profile" page. Shows the hero (logo + name + full prose) plus
// every drop the integration ships, each with its description,
// input/output ports, and a collapsed params hint.
export function IntegrationDetail() {
  const { t } = useTranslation();
  const slugRaw = window.location.pathname.split("/").pop() ?? "";
  const slug = decodeURIComponent(slugRaw);
  const { token } = useAuth();
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
    const m = integrationMeta[slug] ?? {
      name: integrationNameFromSlug(slug),
      description: "",
    };
    return { meta: m, integrationDrops: filtered };
  }, [drops, slug]);

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
        <Link to="/integrations" className="back-link">
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
      <Link to="/integrations" className="back-link">
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
          {meta.technical_notes && (
            <details className="integration-hero-technical">
              <summary>{t("integrations.technicalDetails")}</summary>
              <p>{meta.technical_notes}</p>
            </details>
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

      <h2 className="integration-drops-head">{t("integrations.dropsHead")}</h2>
      <div className="integration-drops">
        {integrationDrops.map((d) => (
          <DropCard key={d.id} drop={d} />
        ))}
      </div>
    </div>
  );
}

// DropCard renders one drop's "help" entry: icon + label + module
// ID, full description, input + output ports, and a collapsed view
// of the params schema (rendered as a JSON dump under a <details>).
function DropCard({ drop }: { drop: Manifest }) {
  const { t } = useTranslation();
  const Icon = iconFor(drop.icon, drop.category);
  const branded = isBrandedIcon(drop.icon);
  const color = drop.color ?? "#9f83fe";
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

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  // .replace stays inside the ES2015 lib target the rest of the
  // app builds against (avoids forcing es2019.string just for trimEnd).
  return s.slice(0, n - 1).replace(/\s+$/, "") + "…";
}
