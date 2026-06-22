import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";
import { FilePlus2, LayoutTemplate, Sparkles } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import { TemplateGallery } from "../components/TemplateGallery";
import type { Graph, Manifest } from "../types";

// GenIssue mirrors core.LintIssue — the heads-up findings the generator
// returns alongside the draft (a missing sheet ID, an app to connect, a
// warning). The server now feeds structural errors back through its own
// repair loop, so what reaches here is the residue worth a human glance.
type GenIssue = { code: string; severity: string; message: string; node_ids?: string[] };

// AI_STARTERS seed the describe box with plain-English examples so a first-time,
// non-technical user isn't staring at a blank field wondering what to type. Each
// maps to a flow the catalog can actually build (Sheets / Gmail / Slack / Stripe).
const AI_STARTERS = [
  "Every weekday at 8am, email me a summary of my Google Sheet",
  "Post new contact form submissions to my Slack #leads channel",
  "Save new Gmail emails to a Google Sheet",
  "Text me when a Stripe payment fails",
];

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
  // AI-first: a non-techy user's fastest path is "describe it, we build it",
  // so that's the default. "Build it myself" stays one click away.
  const [mode, setMode] = useState<"blank" | "ai">("ai");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // pendingDraft holds an AI draft that came back with heads-up issues: we
  // pause on a review step (instead of dropping the user straight into the
  // canvas) so they see "what to check before running" up front.
  const [pendingDraft, setPendingDraft] = useState<{ graph: Graph; issues: GenIssue[] } | null>(null);

  // AI-mode state (mirrors the former CreateWithAIModal).
  const [aiDesc, setAiDesc] = useState("");
  const [needConnect, setNeedConnect] = useState(false);
  const [steps, setSteps] = useState<{ phase: string; message: string }[]>([]);
  const [providers, setProviders] = useState<{ name: string; label: string }[]>([]);
  const [provider, setProvider] = useState(
    () => localStorage.getItem("dazyflow.aiProvider") ?? "",
  );
  // manifests power the plain-language "what this flow does" summary on the
  // review step (module id → friendly label/subtitle). refineText holds the
  // user's plain-English change request.
  const [manifests, setManifests] = useState<Manifest[]>([]);
  const [refineText, setRefineText] = useState("");

  useEffect(() => {
    if (!token) return;
    let live = true;
    api
      .listDrops(token)
      .then((r) => {
        if (live) setManifests(r.drops ?? []);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [token]);

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

  // runGenerate streams a draft and lands on the review step (always — even a
  // clean draft gets a "here's what I built" confirmation before the canvas).
  // base, when set, asks the server to MODIFY that flow (conversational refine).
  const runGenerate = async (genDesc: string, base?: Graph) => {
    if (!token || genDesc.trim() === "" || busy) return;
    setBusy(true);
    setErr(null);
    setNeedConnect(false);
    setSteps([]);
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    let resultGraph: Graph | null = null;
    let resultIssues: GenIssue[] = [];
    let hadError = false;
    try {
      await api.streamFlowGenerate(
        token,
        { description: genDesc.trim(), provider: provider || undefined, tz, base },
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
            const d = data as { graph?: Graph; issues?: GenIssue[] };
            resultGraph = d.graph ?? null;
            resultIssues = d.issues ?? [];
          }
        },
      );
      if (resultGraph) {
        const actionable = resultIssues.filter(
          (i) => i.severity === "error" || i.severity === "warn",
        );
        setPendingDraft({ graph: resultGraph, issues: actionable });
        setRefineText("");
      } else if (!hadError) {
        setErr(t("createAI.empty"));
      }
    } catch (e) {
      setErr((e as Error).message);
    }
    setBusy(false);
  };

  const submitAI = (e: React.FormEvent) => {
    e.preventDefault();
    void runGenerate(aiDesc);
  };

  // Draft-ready review step: show WHAT was built in plain language, surface any
  // heads-up issues, and let the user refine it in plain English — all before
  // they ever touch the node canvas. This is the non-techy heart of the feature.
  if (pendingDraft) {
    const summary = flowSummary(pendingDraft.graph, manifests);
    return (
      <div className="create-flow-scratch">
        <div className="card create-draft-review">
          <h3>{t("createFlow.draftReadyTitle")}</h3>

          {summary.length > 0 && (
            <>
              <p className="create-draft-section">{t("createFlow.whatItDoes")}</p>
              <ol className="create-draft-steps">
                {summary.map((s, i) => (
                  <li key={i}>{s}</li>
                ))}
              </ol>
            </>
          )}

          {pendingDraft.issues.length > 0 && (
            <>
              <p className="create-draft-section">{t("createFlow.beforeItRuns")}</p>
              <ul className="create-draft-issues">
                {pendingDraft.issues.map((is, i) => (
                  <li key={i} className={"create-draft-issue " + is.severity}>
                    <span className="create-draft-issue-head">{friendlyIssueHead(is.code, t)}</span>
                    <span className="create-draft-issue-detail">{is.message}</span>
                  </li>
                ))}
              </ul>
            </>
          )}

          {busy ? (
            <ul className="ai-steps">
              {dedupeSteps(steps).map((s, i, arr) => {
                const active = i === arr.length - 1;
                return (
                  <li key={i} className={active ? "ai-step active" : "ai-step done"}>
                    <span className="ai-step-dot">{active ? "•" : "✓"}</span>
                    {s.message}
                  </li>
                );
              })}
            </ul>
          ) : (
            <form
              className="create-draft-refine"
              onSubmit={(e) => {
                e.preventDefault();
                if (refineText.trim() === "") return;
                void runGenerate(refineText, pendingDraft.graph);
              }}
            >
              <label htmlFor="refine-input" className="create-draft-section">
                {t("createFlow.refineLabel")}
              </label>
              <div className="create-draft-refine-row">
                <input
                  id="refine-input"
                  value={refineText}
                  placeholder={t("createFlow.refinePlaceholder")}
                  onChange={(e) => setRefineText(e.target.value)}
                />
                <button type="submit" className="secondary" disabled={refineText.trim() === ""}>
                  <Sparkles size={14} style={{ marginRight: 6 }} />
                  {t("createFlow.refineCta")}
                </button>
              </div>
            </form>
          )}

          {err && <div className="card" style={{ color: "var(--danger)" }}>{err}</div>}

          <div className="create-flow-actions">
            <button type="button" disabled={busy} onClick={() => setPendingDraft(null)}>
              {t("common.back")}
            </button>
            <button
              type="button"
              className="primary"
              disabled={busy}
              onClick={() => createFromGraph(pendingDraft.graph)}
            >
              {t("createFlow.openDraft")}
            </button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="create-flow-scratch">
      <div className="create-mode-toggle" role="radiogroup" aria-label={t("createFlow.modeLabel")}>
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
          {/* Example prompts: a non-techy user's on-ramp. Shown until they've
              typed something or generation has started; clicking fills the box. */}
          {!busy && steps.length === 0 && aiDesc.trim() === "" && (
            <div className="ai-starters">
              <span className="ai-starters-label">{t("createFlow.startersLabel")}</span>
              <div className="ai-starters-chips">
                {AI_STARTERS.map((s) => (
                  <button
                    key={s}
                    type="button"
                    className="ai-starter-chip"
                    onClick={() => setAiDesc(s)}
                  >
                    {s}
                  </button>
                ))}
              </div>
            </div>
          )}
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
              {/* The agentic loop streams many frames and repeats some verbatim
                  (it reads several steps, validates more than once). Collapse
                  consecutive identical messages so the log reads as clean
                  progress, not a stuck/repeating list. */}
              {dedupeSteps(steps).map((s, i, arr) => {
                const active = i === arr.length - 1 && !err && !needConnect;
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

// flowSummary turns a graph into a plain-language, ordered list of what each
// step does (module id → friendly "Label — subtitle"), so a non-technical user
// sees what was built without reading the node canvas.
function flowSummary(graph: Graph, manifests: Manifest[]): string[] {
  const byId = new Map(manifests.map((m) => [m.id, m]));
  return (graph.nodes ?? []).map((n) => {
    const m = byId.get(n.module);
    if (!m) return n.module;
    return m.subtitle ? `${m.label} — ${m.subtitle}` : m.label;
  });
}

// friendlyIssueHead maps a generator issue code to a short, plain-language
// headline a non-technical user can act on; the raw message follows as detail.
function friendlyIssueHead(code: string, t: (k: string) => string): string {
  switch (code) {
    case "template_placeholder":
      return t("createFlow.issueFillIn");
    case "hardcoded_secret":
      return t("createFlow.issueSecret");
    case "trigger_dropped":
      return t("createFlow.issueSchedule");
    case "invalid_structure":
    case "dangling_reference":
      return t("createFlow.issueWiring");
    default:
      return t("createFlow.issueHeadsUp");
  }
}

// dedupeSteps collapses runs of identical progress messages into one entry, so
// the agentic loop's repeated frames (reading several steps, validating more
// than once) render as a clean activity log.
function dedupeSteps(steps: { phase: string; message: string }[]) {
  const out: { phase: string; message: string }[] = [];
  for (const s of steps) {
    if (out.length === 0 || out[out.length - 1].message !== s.message) {
      out.push(s);
    }
  }
  return out;
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
