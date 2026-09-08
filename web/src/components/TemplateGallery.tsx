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


// A shared topic anyone could subscribe to, so a fork must be given its own
// before it can send anything private.
const SHARED_NTFY_PLACEHOLDER = "my-daily-hello";

export function TemplateGallery() {
  const { t, i18n } = useTranslation();
  const { token, activeTenant, activeWorkspace, hasPerm } = useAuth();
  const canEdit = hasPerm("graph:edit");
  const navigate = useNavigate();
  const [templates, setTemplates] = useState<TemplateSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // template id currently being forked
  // null means not loaded yet, which is not the same as "none configured".
  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(
    null,
  );
  const [searchParams, setSearchParams] = useSearchParams();
  const categoryFilter = searchParams.get("category");
  // A deep link opens one template directly, past the category filter.
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
      // A fresh id: a fork must not collide with the template it came from.
      const suffix = crypto.randomUUID().slice(0, 8);
      const newID = `${tpl.id}-${suffix}`;
      const cloned: Graph = {
        ...tplGraph,
        id: newID,
        tenant: activeTenant,
        workspace: activeWorkspace,
        owner: "",
        // What the fork's hosted form says to visitors, not the forker's UI language.
        language: tplGraph.language || primaryLanguage(i18n.language),
        name: templateTitle(tpl, i18n.language),
        // A placeholder default must be replaced per fork, or every fork shares it.
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

  // Once per mount, or a re-render forks the template again.
  const started = useRef(false);
  useEffect(() => {
    if (!autoStart || started.current || !templates) return;
    // The workspace resolves on a separate async path from the token.
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

// A template needing an integration the operator disabled cannot be forked.
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
