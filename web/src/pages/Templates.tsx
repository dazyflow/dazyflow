import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
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
  const { token, activeTenant, activeWorkspace } = useAuth();
  const navigate = useNavigate();
  const [templates, setTemplates] = useState<TemplateSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // template id currently being forked

  useEffect(() => {
    api
      .listTemplates()
      .then((r) => setTemplates(r.templates))
      .catch((e: Error) => setError(e.message));
  }, []);

  const useTemplate = async (tpl: TemplateSummary) => {
    if (!token || !activeTenant || !activeWorkspace) {
      setError("Not signed in.");
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
      setError(`Couldn't fork "${tpl.title}": ${msg}`);
    } finally {
      setBusy(null);
    }
  };

  if (error && !templates) {
    return (
      <div className="page">
        <h1>Templates</h1>
        <div className="card error">{error}</div>
      </div>
    );
  }
  if (!templates) {
    return (
      <div className="page">
        <h1>Templates</h1>
        <div className="card">Loading…</div>
      </div>
    );
  }

  return (
    <div className="page templates-page">
      <h1>Templates</h1>
      <p className="page-sub">
        Pre-built workflows you can fork in one click. Each one lands
        in your workspace as a normal graph — edit, run, or rename it
        like any other.
      </p>
      {error && <div className="card error" style={{ marginBottom: 12 }}>{error}</div>}
      <div className="template-grid">
        {templates.map((tpl) => {
          const Icon = iconFor(tpl.icon);
          return (
            <div key={tpl.id} className="template-card">
              <div className="template-card-head">
                <span className="template-icon">
                  <Icon size={18} strokeWidth={2.2} />
                </span>
                <h2>{tpl.title}</h2>
              </div>
              <p className="template-desc">{tpl.description}</p>
              {tpl.tags && tpl.tags.length > 0 && (
                <div className="template-tags">
                  {tpl.tags.map((t) => (
                    <span key={t} className="template-tag">
                      {t}
                    </span>
                  ))}
                </div>
              )}
              <button
                type="button"
                className="primary template-cta"
                onClick={() => useTemplate(tpl)}
                disabled={busy !== null}
              >
                {busy === tpl.id ? "Forking…" : "Use this template"}
              </button>
            </div>
          );
        })}
      </div>
    </div>
  );
}
