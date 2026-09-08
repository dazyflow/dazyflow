// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams, Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { primaryLanguage } from "../lib/language";
import { useAuth } from "../auth";
import { iconFor, ICON } from "../icons";
import {
  displayNameForIntegrationSlug,
  oauthProviderForIntegration,
} from "../integrationMeta";
import { browserTimeZone } from "./editor/TriggersModal";
import { Button } from "./ui/Button";
import type { Graph, OAuthProviderStatus, TemplateSummary } from "../types";
import {
  templateBlurb,
  templateCategory,
  templateTitle,
} from "../lib/templateText";
import { integrationName } from "../lib/dropText";
import { ErrorNotice } from "./ui/ErrorNotice";
import { Loading } from "./ui/Loading";


// SHARED_NTFY_PLACEHOLDER is the topic an ntfy template would ship with.
// It's a guessable, world-readable shared topic, so applyTemplate swaps it
// for an unguessable per-fork topic on fork (see the nodes map below).
// Kept for forward-compat with any ntfy template re-added to the gallery.
const SHARED_NTFY_PLACEHOLDER = "my-daily-hello";

export function TemplateGallery() {
  const { t, i18n } = useTranslation();
  const { token, activeTenant, activeWorkspace, hasPerm } = useAuth();
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
  const categoryFilter = searchParams.get("category");
  // ?template=<id> is a tighter focus than category: a deep-link can open
  // the gallery on one template instead of a category list it would
  // otherwise be buried in. Takes precedence over category.
  const templateFilter = searchParams.get("template");
  const autoStart = searchParams.get("start");

  useEffect(() => {
    api
      .listTemplates()
      .then((r) => setTemplates(r.templates))
      .catch((e) => setError(explainApiError(e, t)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
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

  const availableProviders = useMemo(() => {
    if (providers === null) return null;
    return new Set(providers.map((p) => p.name));
  }, [providers]);

  const applyTemplate = async (tpl: TemplateSummary) => {
    if (!token) {
      setError(t("templates.notSignedIn"));
      return;
    }
    if (!activeTenant || !activeWorkspace) {
      setError(t("templates.workspaceLoading"));
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
        owner: "",
        // The flow's OUTPUT language: what its hosted form says to visitors
        // ("Submit", "Thanks!"), and what steps that spell out words write.
        // Empty means English, so a Swedish owner forking a template used to
        // publish an English form to their Swedish customers without ever
        // being shown a language control. Stamp the forker's own language, the
        // same way the time zone below is stamped. A template that names a
        // language deliberately keeps it, and the owner can change it in
        // Settings → General.
        language: tplGraph.language || primaryLanguage(i18n.language),
        name: templateTitle(tpl, i18n.language),
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
      setError(
        t("templates.forkFailed", {
          title: templateTitle(tpl, i18n.language),
          error: msg,
        }),
      );
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
    if (!autoStart || started.current || !templates) return;
    // The workspace, not just the token, is what applyTemplate needs. The
    // template index is a static fetch and resolves well before the identity
    // bootstrap, so gating on `token` alone fired this on a cold load with
    // activeTenant still "" — and the user's very first action reported
    // "not signed in" while signed in. Waiting costs nothing: the effect
    // re-runs when the bootstrap lands.
    if (!token || !activeTenant || !activeWorkspace) return;
    const tpl = templates.find((x) => x.id === autoStart);
    started.current = true;
    const sp = new URLSearchParams(searchParams);
    sp.delete("start");
    setSearchParams(sp, { replace: true });
    if (tpl) void applyTemplate(tpl);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoStart, templates, token, activeTenant, activeWorkspace]);

  if (error && !templates) {
    return <ErrorNotice>{error}</ErrorNotice>;
  }
  if (!templates) {
    return <Loading />;
  }

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
    const cat = tpl.category?.trim() ?? "";
    let g = groups.find((x) => x.category === cat);
    if (!g) {
      g = { category: cat, items: [] };
      groups.push(g);
    }
    g.items.push(tpl);
  }

  return (
    <div>
      {(categoryFilter || templateFilter) && (
        <div className="template-filter-chip">
          <span>
            {templateFilter
              ? t("templates.filteredByTemplate", {
                  title: focusedTpl
                    ? templateTitle(focusedTpl, i18n.language)
                    : templateFilter,
                })
              : t("templates.filteredBy", {
                  category: templateCategory(categoryFilter ?? "", i18n.language),
                })}
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
      {error && <ErrorNotice style={{ marginBottom: "var(--space-3)" }}>{error}</ErrorNotice>}
      {groups.length === 0 && (
        <div className="card">
          {t("templates.noneInCategory")}{" "}
          <Link to="/flows/new?tab=template">{t("templates.showAll")}</Link>
        </div>
      )}
      {groups.map((group) => (
        <section key={group.category} className="template-group">
          <h2 className="template-group-head">
            {group.category
              ? templateCategory(group.category, i18n.language)
              : t("templates.uncategorized")}
          </h2>
          <div className="template-grid">
            {group.items.map((tpl) => {
              const Icon = iconFor(tpl.icon);
              const missingIntegrationNames =
                availableProviders === null
                  ? []
                  : oauthBlockedIntegrations(
                      tpl.integrations ?? [],
                      availableProviders,
                      i18n.language,
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
                    <h3>{templateTitle(tpl, i18n.language)}</h3>
                  </div>
                  {tpl.integrations && tpl.integrations.length > 0 && (
                    <TemplateIntegrationRow slugs={tpl.integrations} />
                  )}
                  <p className="template-desc">
                    {templateBlurb(tpl, i18n.language)}
                  </p>
                  {adminBlocked && (
                    <p className="template-admin-blocked-note">
                      {t("templates.adminBlocked", {
                        count: missingIntegrationNames.length,
                        names: missingIntegrationNames.join(", "),
                      })}
                    </p>
                  )}
                  <Button
                    variant="primary"
                    className="template-cta"
                    onClick={() => applyTemplate(tpl)}
                    disabled={busy !== null || adminBlocked || !canEdit}
                    title={
                      adminBlocked
                        ? t("templates.adminBlockedTitle", {
                            count: missingIntegrationNames.length,
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
  lang?: string,
): string[] {
  const out = new Set<string>();
  for (const slug of integrationSlugs) {
    const provider = oauthProviderForIntegration(slug);
    if (!provider) continue;
    if (availableProviders.has(provider)) continue;
    out.add(integrationName(displayNameForIntegrationSlug(slug), lang));
  }
  return [...out].sort();
}

const templateIntegrationCap = 4;

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
