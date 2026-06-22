import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";
import { FilePlus2, LayoutTemplate, Sparkles } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import { TemplateGallery } from "../components/TemplateGallery";
import type { Graph } from "../types";

// CreateFlow is the single surface for starting a new flow. Two tabs:
//  - "From scratch": name a blank flow, or describe one and let AI draft it.
//  - "From a template": the pre-built gallery (TemplateGallery), forked in
//    one click.
// It replaces the old standalone /templates page and the three competing
// create buttons that used to live on the Flows list. The active tab is
// reflected in ?tab=scratch|template so the /templates redirect and any
// deep-link can open straight onto the gallery.
export function CreateFlow() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = searchParams.get("tab") === "template" ? "template" : "scratch";
  const setTab = (next: "scratch" | "template") => {
    const sp = new URLSearchParams(searchParams);
    sp.set("tab", next);
    setSearchParams(sp, { replace: true });
  };

  return (
    <div className="page create-flow">
      <h1>{t("createFlow.title")}</h1>
      <div className="create-flow-tabs" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "scratch"}
          className={"create-flow-tab" + (tab === "scratch" ? " active" : "")}
          onClick={() => setTab("scratch")}
        >
          <FilePlus2 size={16} />
          {t("createFlow.tabScratch")}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "template"}
          className={"create-flow-tab" + (tab === "template" ? " active" : "")}
          onClick={() => setTab("template")}
        >
          <LayoutTemplate size={16} />
          {t("createFlow.tabTemplate")}
        </button>
      </div>
      {tab === "scratch" ? <FromScratch /> : <TemplateGallery />}
    </div>
  );
}

// FromScratch holds the blank-vs-AI creation form. "Blank" saves an empty
// graph and opens the editor; "AI" streams a draft from the server (same
// grounded+validated path the old modal used) and opens it for review —
// nothing is run either way.
function FromScratch() {
  const { t } = useTranslation();
  const { token, me, activeTenant, activeWorkspace, hasPerm } = useAuth();
  const canEdit = hasPerm("graph:edit");
  const navigate = useNavigate();
  const [mode, setMode] = useState<"blank" | "ai">("blank");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // AI-mode state (mirrors the former CreateWithAIModal).
  const [aiDesc, setAiDesc] = useState("");
  const [needConnect, setNeedConnect] = useState(false);
  const [steps, setSteps] = useState<{ phase: string; message: string }[]>([]);
  const [providers, setProviders] = useState<{ name: string; label: string }[]>([]);
  const [provider, setProvider] = useState(
    () => localStorage.getItem("dazyflow.aiProvider") ?? "",
  );

  useEffect(() => {
    if (!token || mode !== "ai") return;
    let live = true;
    api
      .listLLMProviders(token)
      .then((r) => {
        if (!live) return;
        const list = r.providers ?? [];
        setProviders(list);
        setProvider((cur) =>
          list.some((p) => p.name === cur) ? cur : (list[0]?.name ?? ""),
        );
        setNeedConnect(list.length === 0);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [token, mode]);

  // createNew persists an empty graph (machine ID derived from the name so
  // the user never types a slug) and opens it in the editor.
  const createNew = async () => {
    if (!token || !me || !activeWorkspace) return;
    const id = `${slugify(name)}-${Math.random().toString(36).slice(2, 8)}`;
    await api.saveGraph(token, {
      id,
      tenant: activeTenant,
      workspace: activeWorkspace,
      nodes: [],
      edges: [],
      name: name.trim(),
      description: description.trim() || undefined,
    });
    navigate(`/flows/${encodeURIComponent(id)}`);
  };

  // createFromGraph persists an AI-generated DRAFT and opens it in the editor
  // for review. Same save+open path as a blank flow — it is NOT run; the user
  // reviews, tweaks, and publishes when ready.
  const createFromGraph = async (graph: Graph) => {
    if (!token || !activeWorkspace) return;
    const flowName = (graph.name || "AI-generated flow").trim();
    const id = `${slugify(flowName)}-${Math.random().toString(36).slice(2, 8)}`;
    await api.saveGraph(token, {
      id,
      tenant: activeTenant,
      workspace: activeWorkspace,
      nodes: graph.nodes ?? [],
      edges: graph.edges ?? [],
      name: flowName,
      description: graph.description,
    });
    navigate(`/flows/${encodeURIComponent(id)}`);
  };

  const submitBlank = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await createNew();
    } catch (e) {
      setErr((e as Error).message);
      setBusy(false);
    }
  };

  const submitAI = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || aiDesc.trim() === "" || busy) return;
    setBusy(true);
    setErr(null);
    setNeedConnect(false);
    setSteps([]);
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    let resultGraph: Graph | null = null;
    let hadError = false;
    try {
      await api.streamFlowGenerate(
        token,
        { description: aiDesc.trim(), provider: provider || undefined, tz },
        (kind, data) => {
          if (kind === "progress") {
            const d = data as { phase: string; message: string };
            setSteps((s) => [...s, d]);
          } else if (kind === "error") {
            hadError = true;
            const d = data as { message?: string; need_connect?: boolean };
            if (d.need_connect) setNeedConnect(true);
            else setErr(d.message ?? t("createAI.empty"));
          } else if (kind === "done") {
            resultGraph = (data as { graph?: Graph }).graph ?? null;
          }
        },
      );
      if (resultGraph) {
        // Hand the draft to save + open editor. Keep busy — about to navigate.
        await createFromGraph(resultGraph);
        return;
      }
      if (!hadError) setErr(t("createAI.empty"));
    } catch (e) {
      setErr((e as Error).message);
    }
    setBusy(false);
  };

  return (
    <div className="create-flow-scratch">
      <div className="create-mode-toggle" role="radiogroup" aria-label={t("createFlow.modeLabel")}>
        <label className={"create-mode-option" + (mode === "blank" ? " active" : "")}>
          <input
            type="radio"
            name="create-mode"
            checked={mode === "blank"}
            onChange={() => setMode("blank")}
          />
          <FilePlus2 size={15} />
          <span>{t("createFlow.modeBlank")}</span>
        </label>
        <label className={"create-mode-option" + (mode === "ai" ? " active" : "")}>
          <input
            type="radio"
            name="create-mode"
            checked={mode === "ai"}
            onChange={() => setMode("ai")}
          />
          <Sparkles size={15} />
          <span>{t("createFlow.modeAI")}</span>
        </label>
      </div>

      {mode === "blank" ? (
        <form className="create-flow-card card" onSubmit={submitBlank}>
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="new-flow-name">{t("flowList.nameLabel")}</label>
            </div>
            <input
              id="new-flow-name"
              autoFocus
              value={name}
              placeholder={t("flowList.namePlaceholder")}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="new-flow-desc">{t("flowList.descLabel")}</label>
            </div>
            <input
              id="new-flow-desc"
              value={description}
              placeholder={t("flowList.descPlaceholder")}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          {err && (
            <div className="card" style={{ color: "var(--danger)" }}>{err}</div>
          )}
          <div className="create-flow-actions">
            <button
              type="submit"
              className="primary"
              disabled={busy || !name.trim() || !canEdit}
              title={!canEdit ? t("flowList.needEdit") : undefined}
            >
              {busy ? t("flowList.creating") : t("flowList.createCta")}
            </button>
          </div>
        </form>
      ) : (
        <form className="create-flow-card card" onSubmit={submitAI}>
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="ai-flow-desc">{t("createAI.label")}</label>
            </div>
            <textarea
              id="ai-flow-desc"
              autoFocus
              rows={4}
              value={aiDesc}
              placeholder={t("createAI.placeholder")}
              onChange={(e) => setAiDesc(e.target.value)}
            />
            <div className="sf-hint">{t("createAI.hint")}</div>
          </div>
          {providers.length > 1 && (
            <div className="sf-field">
              <div className="label-row">
                <label htmlFor="ai-flow-provider">{t("createAI.providerLabel")}</label>
              </div>
              <select
                id="ai-flow-provider"
                value={provider}
                onChange={(e) => {
                  setProvider(e.target.value);
                  localStorage.setItem("dazyflow.aiProvider", e.target.value);
                }}
              >
                {providers.map((p) => (
                  <option key={p.name} value={p.name}>
                    {p.label}
                  </option>
                ))}
              </select>
            </div>
          )}
          {busy && steps.length > 0 && (
            <ul className="ai-steps">
              {steps.map((s, i) => {
                const active = i === steps.length - 1 && !err && !needConnect;
                return (
                  <li key={i} className={active ? "ai-step active" : "ai-step done"}>
                    <span className="ai-step-dot">{active ? "•" : "✓"}</span>
                    {s.message}
                  </li>
                );
              })}
            </ul>
          )}
          {needConnect && (
            <div className="card" style={{ color: "var(--danger)" }}>
              <Trans i18nKey="createAI.needConnect" components={[<Link to="/apps" />]} />
            </div>
          )}
          {err && <div className="card" style={{ color: "var(--danger)" }}>{err}</div>}
          <div className="create-flow-actions">
            <button
              type="submit"
              className="primary"
              disabled={busy || aiDesc.trim() === "" || needConnect || !canEdit}
              title={!canEdit ? t("flowList.needEdit") : undefined}
            >
              <Sparkles size={14} style={{ marginRight: 6 }} />
              {busy ? t("createAI.generating") : t("createAI.generate")}
            </button>
          </div>
        </form>
      )}
    </div>
  );
}

// slugify turns a human flow name into the [A-Za-z0-9_.-] machine ID the
// daemon expects. Lowercases, swaps runs of anything else for a single
// hyphen, trims edge hyphens, and falls back to "flow" when a name is
// all punctuation/empty.
function slugify(name: string): string {
  const s = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return s || "flow";
}
