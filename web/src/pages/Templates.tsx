import { useEffect, useState } from "react";
import { useNavigate, useSearchParams, Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { iconFor } from "../icons";
import type { Graph, TemplateSummary } from "../types";

// Templates is the gallery page: lists pre-built workflows the user
// can fork into their own workspace with one click. On click we
// fetch the template's graph file, generate a fresh graph ID, fill
// in the user's tenant + workspace, and PUT through the normal
// saveGraph endpoint — same code path as creating a graph by hand,
// just pre-populated with nodes + edges.
//
// The gallery itself is static (web/public/templates/index.json).
// Adding a template is a JSON file + a one-line index entry; no
// daemon code change.
export function Templates() {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace } = useAuth();
  const navigate = useNavigate();
  const [templates, setTemplates] = useState<TemplateSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // template id currently being forked
  const [showTech, setShowTech] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  // Goal-first entry from the Welcome page lands here with ?category=…
  // so the gallery opens already narrowed to the user's intent.
  const categoryFilter = searchParams.get("category");

  useEffect(() => {
    api
      .listTemplates()
      .then((r) => setTemplates(r.templates))
      .catch((e: Error) => setError(e.message));
  }, []);

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
      // template ID plus a short suffix so multiple forks of the
      // same template don't collide.
      const suffix = Math.random().toString(36).slice(2, 8);
      const newID = `${tpl.id}-${suffix}`;
      const cloned: Graph = {
        ...tplGraph,
        id: newID,
        tenant: activeTenant,
        workspace: activeWorkspace,
        // owner intentionally left blank — the daemon stamps the
        // caller as owner on first save.
        owner: "",
      };
      await api.saveGraph(token, cloned);
      navigate(`/flows/${encodeURIComponent(newID)}`);
    } catch (e) {
      const msg = e instanceof APIError ? `${e.status}: ${e.message}` : (e as Error).message;
      setError(t("templates.forkFailed", { title: tpl.title, error: msg }));
    } finally {
      setBusy(null);
    }
  };

  if (error && !templates) {
    return (
      <div className="page">
        <h1>{t("templates.title")}</h1>
        <div className="card error">{error}</div>
      </div>
    );
  }
  if (!templates) {
    return (
      <div className="page">
        <h1>{t("templates.title")}</h1>
        <div className="card">{t("common.loading")}</div>
      </div>
    );
  }

  // Group cards under their category heading so a non-technical visitor
  // can scan by intent ("Get notified", "Scheduled reports"). Order is
  // driven by first appearance in the index file, so curation controls
  // the layout without a hard-coded category list here. Entries with no
  // category fall into a catch-all bucket rendered last.
  const visible = categoryFilter
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
    <div className="page templates-page">
      <h1>{t("templates.title")}</h1>
      <p className="page-sub">{t("templates.intro")}</p>
      {categoryFilter && (
        <div className="template-filter-chip">
          <span>{t("templates.filteredBy", { category: categoryFilter })}</span>
          <button
            type="button"
            className="ghost"
            onClick={() => {
              searchParams.delete("category");
              setSearchParams(searchParams, { replace: true });
            }}
          >
            {t("templates.showAll")}
          </button>
        </div>
      )}
      <label className="template-tech-toggle">
        <input
          type="checkbox"
          checked={showTech}
          onChange={(e) => setShowTech(e.target.checked)}
        />
        {t("templates.showTechnical")}
      </label>
      {error && <div className="card error" style={{ marginBottom: 12 }}>{error}</div>}
      {groups.length === 0 && (
        <div className="card">
          {t("templates.noneInCategory")}{" "}
          <Link to="/templates">{t("templates.showAll")}</Link>
        </div>
      )}
      {groups.map((group) => (
        <section key={group.category} className="template-group">
          <h2 className="template-group-head">{group.category}</h2>
          <div className="template-grid">
            {group.items.map((tpl) => {
              const Icon = iconFor(tpl.icon);
              return (
                <div key={tpl.id} className="template-card">
                  <div className="template-card-head">
                    <span className="template-icon">
                      <Icon size={18} strokeWidth={2.2} />
                    </span>
                    <h3>{tpl.title}</h3>
                  </div>
                  {tpl.integrations && tpl.integrations.length > 0 && (
                    <TemplateIntegrationRow slugs={tpl.integrations} />
                  )}
                  <p className="template-desc">{tpl.use_case || tpl.description}</p>
                  {showTech && (
                    <div className="template-tech">
                      {tpl.use_case && (
                        <p className="template-tech-desc">{tpl.description}</p>
                      )}
                      {tpl.tags && tpl.tags.length > 0 && (
                        <div className="template-tags">
                          {tpl.tags.map((tag) => (
                            <span key={tag} className="template-tag">
                              {tag}
                            </span>
                          ))}
                        </div>
                      )}
                    </div>
                  )}
                  <button
                    type="button"
                    className="primary template-cta"
                    onClick={() => useTemplate(tpl)}
                    disabled={busy !== null}
                  >
                    {busy === tpl.id ? t("templates.forking") : t("templates.useTemplate")}
                  </button>
                </div>
              );
            })}
          </div>
        </section>
      ))}
    </div>
  );
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
