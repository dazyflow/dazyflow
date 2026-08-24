// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams, Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { useAuth } from "../auth";
import { iconFor, ICON } from "../icons";
import {
  displayNameForIntegrationSlug,
  oauthProviderForIntegration,
} from "../integrationMeta";
import { browserTimeZone } from "./editor/TriggersModal";
import { Button } from "./ui/Button";
import type { Graph, OAuthProviderStatus, TemplateSummary } from "../types";
import { ErrorNotice } from "./ui/ErrorNotice";

// TemplateGallery is the reusable card grid of pre-built workflows. It
// lives inside the "From a template" tab of the Create-flow page (and is
// reached via the /templates → /flows/new?tab=template redirect). On click
// we fetch the template's graph file, generate a fresh graph ID, fill in
// the user's tenant + workspace, and PUT through the normal saveGraph
// endpoint — same code path as creating a graph by hand, just pre-populated
// with nodes + edges.
//
// The gallery itself is static (web/public/templates/index.json). Adding a
// template is a JSON file + a one-line index entry; no daemon code change.

// SHARED_NTFY_PLACEHOLDER is the topic an ntfy template would ship with.
// It's a guessable, world-readable shared topic, so useTemplate swaps it
// for an unguessable per-fork topic on fork (see the nodes map below).
// Kept for forward-compat with any ntfy template re-added to the gallery.
const SHARED_NTFY_PLACEHOLDER = "my-daily-hello";

export function TemplateGallery() {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace, hasPerm } = useAuth();
  // Forking a template creates a flow → needs graph:edit. Viewers can
  // browse templates but the "Use this template" action is disabled.
  const canEdit = hasPerm("graph:edit");
  const navigate = useNavigate();
  const [templates, setTemplates] = useState<TemplateSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // template id currently being forked
  // providers is the OAuth catalog for this install. null = not loaded
  // yet (or feature unavailable on this hosted box, which the daemon
  // signals with 501). When the catalog is empty/null, OAuth-needing
  // templates are flagged as admin-blocked so a non-tech buyer doesn't
  // fork into a setup that can't run end-to-end.
  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(
    null,
  );
  const [searchParams, setSearchParams] = useSearchParams();
  // Goal-first entry can land here with ?category=… so the gallery opens
  // already narrowed to the user's intent.
  const categoryFilter = searchParams.get("category");
  // ?template=<id> is a tighter focus than category: a deep-link can open
  // the gallery on one template instead of a category list it would
  // otherwise be buried in. Takes precedence over category.
  const templateFilter = searchParams.get("template");
  // ?start=<id> goes one step further and COPIES that template immediately,
  // landing the user in the editor with a working flow. Welcome's primary
  // action uses it for the no-setup starter: the fastest honest answer to
  // "what is this thing?" is a flow on screen, not a form to fill in.
  const autoStart = searchParams.get("start");

  useEffect(() => {
    api
      .listTemplates()
      .then((r) => setTemplates(r.templates))
      .catch((e) => setError(explainApiError(e, t)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Fetch OAuth providers in parallel — kept independent so a 501 or
  // 401 here doesn't blank the whole templates gallery. The map of
  // available provider names drives per-card admin-blocked badges.
  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    api
      .listProviders(token)
      .then((r) => {
        if (!cancelled) setProviders(r.providers);
      })
      .catch(() => {
        // 501 = OAuth not configured. Same outcome as "no providers
        // available" for filtering: any OAuth-needing template gets
        // flagged. Stash the empty list so the gating logic kicks in.
        if (!cancelled) setProviders([]);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  // availableProviders is the set of OAuth provider names this install
  // currently exposes. Empty when the feature is off or the daemon
  // hasn't reported any configured providers — the same case from the
  // template-gating POV.
  const availableProviders = useMemo(() => {
    if (providers === null) return null;
    return new Set(providers.map((p) => p.name));
  }, [providers]);

  const useTemplate = async (tpl: TemplateSummary) => {
    if (!token || !activeTenant || !activeWorkspace) {
      setError(t("templates.notSignedIn"));
      return;
    }
    setBusy(tpl.id);
    setError(null);
    try {
      const tplGraph: Graph = await api.loadTemplateGraph(tpl.graph_file);
      // Generate a fresh ID — keep a human-readable slug from the
      // template ID plus a short suffix so multiple forks of the same
      // template don't collide. crypto.randomUUID() is collision-resistant
      // (the old Math.random slug could repeat and silently overwrite an
      // existing fork via the saveGraph PUT); take the first UUID segment
      // for a short, readable suffix.
      const suffix = crypto.randomUUID().slice(0, 8);
      const newID = `${tpl.id}-${suffix}`;
      const cloned: Graph = {
        ...tplGraph,
        id: newID,
        tenant: activeTenant,
        workspace: activeWorkspace,
        // owner intentionally left blank — the daemon stamps the
        // caller as owner on first save.
        owner: "",
        // Per-fork personalisation of nodes that ship a placeholder default:
        //  - cron_trigger: stamp the forker's time zone. Templates are
        //    zone-neutral (a shared "0 9 * * *" means 9am wherever you are),
        //    so the fork is where it gets personalised — otherwise both the
        //    schedule and its fired_at would run in UTC.
        //  - ntfy: stamp a unique topic. ntfy topics are world-readable —
        //    anyone who knows the topic can read (and publish to) it — and
        //    templates ship a shared placeholder topic, so every fork would
        //    otherwise push to (and receive) the same global ntfy.sh topic.
        //    Give each fork an unguessable topic; the user can still change
        //    it in the editor.
        nodes: (tplGraph.nodes ?? []).map((n) => {
          if (n.module === "cron_trigger") {
            const tz = (n.params as { tz?: unknown } | undefined)?.tz;
            if (!(typeof tz === "string" && tz.trim())) {
              return { ...n, params: { ...(n.params ?? {}), tz: browserTimeZone() } };
            }
            return n;
          }
          if (n.module === "ntfy") {
            const topic = (n.params as { topic?: unknown } | undefined)?.topic;
            const t = typeof topic === "string" ? topic.trim() : "";
            if (t === "" || t === SHARED_NTFY_PLACEHOLDER) {
              return {
                ...n,
                params: {
                  ...(n.params ?? {}),
                  topic: `dazyflow-${crypto.randomUUID().slice(0, 8)}`,
                },
              };
            }
            return n;
          }
          return n;
        }),
      };
      await api.saveGraph(token, cloned);
      navigate(`/flows/${encodeURIComponent(newID)}`);
    } catch (e) {
      const msg = explainApiError(e, t);
      setError(t("templates.forkFailed", { title: tpl.title, error: msg }));
    } finally {
      setBusy(null);
    }
  };

  // Auto-start: copy ?start=<id> as soon as the list resolves, once per mount.
  // The ref (not state) is what makes it once — a re-render mid-copy must not
  // fire a second saveGraph, and going Back to this URL should not silently
  // mint another flow, so we also strip the param as we go. An id the index
  // doesn't know is ignored: the user just sees the normal gallery.
  const started = useRef(false);
  useEffect(() => {
    if (!autoStart || started.current || !templates || !token) return;
    const tpl = templates.find((x) => x.id === autoStart);
    started.current = true;
    const sp = new URLSearchParams(searchParams);
    sp.delete("start");
    setSearchParams(sp, { replace: true });
    if (tpl) void useTemplate(tpl);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoStart, templates, token]);

  if (error && !templates) {
    return <ErrorNotice>{error}</ErrorNotice>;
  }
  if (!templates) {
    return <div className="card">{t("common.loading")}</div>;
  }

  // Group cards under their category heading so a non-technical visitor
  // can scan by intent ("Get notified", "Scheduled reports"). Order is
  // driven by first appearance in the index file, so curation controls
  // the layout without a hard-coded category list here. Entries with no
  // category fall into a catch-all bucket rendered last.
  // focusedTpl resolves the ?template= id to its summary so the filter
  // chip can name it; null when the param is absent or unknown.
  const focusedTpl = templateFilter
    ? templates.find((tpl) => tpl.id === templateFilter) ?? null
    : null;
  const visible = templateFilter
    ? templates.filter((tpl) => tpl.id === templateFilter)
    : categoryFilter
      ? templates.filter((tpl) => tpl.category === categoryFilter)
      : templates;
  const groups: { category: string; items: TemplateSummary[] }[] = [];
  for (const tpl of visible) {
    const cat = tpl.category?.trim() || t("templates.uncategorized");
    let g = groups.find((x) => x.category === cat);
    if (!g) {
      g = { category: cat, items: [] };
      groups.push(g);
    }
    g.items.push(tpl);
  }

  return (
    <div className="template-gallery">
      {(categoryFilter || templateFilter) && (
        <div className="template-filter-chip">
          <span>
            {templateFilter
              ? t("templates.filteredByTemplate", {
                  title: focusedTpl?.title ?? templateFilter,
                })
              : t("templates.filteredBy", { category: categoryFilter })}
          </span>
          <Button
            variant="ghost"
            onClick={() => {
              searchParams.delete("category");
              searchParams.delete("template");
              setSearchParams(searchParams, { replace: true });
            }}
          >
            {t("templates.showAll")}
          </Button>
        </div>
      )}
      {error && <ErrorNotice style={{ marginBottom: 12 }}>{error}</ErrorNotice>}
      {groups.length === 0 && (
        <div className="card">
          {t("templates.noneInCategory")}{" "}
          <Link to="/flows/new?tab=template">{t("templates.showAll")}</Link>
        </div>
      )}
      {groups.map((group) => (
        <section key={group.category} className="template-group">
          <h2 className="template-group-head">{group.category}</h2>
          <div className="template-grid">
            {group.items.map((tpl) => {
              const Icon = iconFor(tpl.icon);
              // missingIntegrationNames lists the OAuth-backed
              // integrations this template references but that the
              // current install hasn't enabled. Empty list = template
              // is forkable. Computed against availableProviders,
              // which itself is null until the providers fetch
              // resolves — until then we don't block, so the gallery
              // doesn't briefly grey every card on a slow network.
              const missingIntegrationNames =
                availableProviders === null
                  ? []
                  : oauthBlockedIntegrations(
                      tpl.integrations ?? [],
                      availableProviders,
                    );
              const adminBlocked = missingIntegrationNames.length > 0;
              return (
                <div
                  key={tpl.id}
                  className={
                    "template-card" +
                    (adminBlocked ? " template-card-admin-blocked" : "")
                  }
                >
                  <div className="template-card-head">
                    <span className="template-icon">
                      <Icon size={ICON.lg} strokeWidth={2.2} />
                    </span>
                    <h3>{tpl.title}</h3>
                  </div>
                  {tpl.integrations && tpl.integrations.length > 0 && (
                    <TemplateIntegrationRow slugs={tpl.integrations} />
                  )}
                  <p className="template-desc">{tpl.use_case || tpl.description}</p>
                  {adminBlocked && (
                    <p className="template-admin-blocked-note">
                      {t("templates.adminBlocked", {
                        names: missingIntegrationNames.join(", "),
                      })}
                    </p>
                  )}
                  <Button
                    variant="primary"
                    className="template-cta"
                    onClick={() => useTemplate(tpl)}
                    disabled={busy !== null || adminBlocked || !canEdit}
                    title={
                      adminBlocked
                        ? t("templates.adminBlockedTitle", {
                            names: missingIntegrationNames.join(", "),
                          })
                        : !canEdit
                          ? t("flowList.needEdit")
                          : undefined
                    }
                  >
                    {busy === tpl.id ? t("templates.forking") : t("templates.useTemplate")}
                  </Button>
                </div>
              );
            })}
          </div>
        </section>
      ))}
    </div>
  );
}

// oauthBlockedIntegrations names the template-listed integrations
// whose OAuth provider isn't enabled on this install. For each entry
// in the template's `integrations` array we look up the corresponding
// OAuth provider; if the provider exists AND isn't in the available
// set, we surface the integration's display name ("Gmail", "Slack")
// — not the provider key — because that's what the user already sees
// in the card's brand-logo row, so the message lines up. Integrations
// with no OAuth mapping (postgres, sqlite, webhook, ...) are skipped
// here; the editor's pre-run banner covers the secret-store side.
function oauthBlockedIntegrations(
  integrationSlugs: string[],
  availableProviders: Set<string>,
): string[] {
  const out = new Set<string>();
  for (const slug of integrationSlugs) {
    const provider = oauthProviderForIntegration(slug);
    if (!provider) continue;
    if (availableProviders.has(provider)) continue;
    out.add(displayNameForIntegrationSlug(slug));
  }
  return [...out].sort();
}

// templateIntegrationCap is how many brand logos we render before
// collapsing the rest into a "+N" indicator. Four keeps cards visually
// tidy at the most common widths; templates with more integrations
// still surface that they're touching multiple services.
const templateIntegrationCap = 4;

// TemplateIntegrationRow draws a small row of vendor brand icons on
// the template card so users can scan "this template touches Gmail
// and Slack" without reading the title. Each slug maps 1:1 to
// /brands/<slug>.svg under the public assets root; missing files
// produce a broken-image (caught at content-curation time, not a
// render hazard).
function TemplateIntegrationRow({ slugs }: { slugs: string[] }) {
  const { t } = useTranslation();
  const shown = slugs.slice(0, templateIntegrationCap);
  const overflow = slugs.length - shown.length;
  return (
    <div className="template-integrations" aria-label={t("templates.integrationsUsed")}>
      {shown.map((slug) => (
        <img
          key={slug}
          src={`/brands/${slug}.svg`}
          alt={slug}
          title={slug}
          className="template-integration-logo"
          draggable={false}
        />
      ))}
      {overflow > 0 && (
        <span className="template-integration-more" title={slugs.slice(templateIntegrationCap).join(", ")}>
          +{overflow}
        </span>
      )}
    </div>
  );
}
