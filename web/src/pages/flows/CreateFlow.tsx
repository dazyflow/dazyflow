// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Bell, FilePlus2, LayoutTemplate, Mail, MessageSquare, Sheet, Sparkles } from "lucide-react";
import { useAuth } from "../../auth";
import { api } from "../../api";
import { dropLabel, dropSubtitle } from "../../lib/dropText";
import { TemplateGallery } from "../../components/TemplateGallery";
import { Callout } from "../../components/ui/Callout";
import { Button } from "../../components/ui/Button";
import { explainApiError } from "../../lib/explainApiError";
import type { Graph, Manifest } from "../../types";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { slugify } from "../../lib/format";

// GenIssue mirrors core.LintIssue — the heads-up findings the generator
// returns alongside the draft (a missing sheet ID, an app to connect, a
// warning). The server now feeds structural errors back through its own
// repair loop, so what reaches here is the residue worth a human glance.
type GenIssue = { code: string; severity: string; message: string; node_ids?: string[] };

// AI_STARTERS seed the describe box with plain-English examples so a first-time,
// non-technical user isn't staring at a blank field wondering what to type. Each
// maps to a flow the catalog can actually build (Sheets / Gmail / Slack / Stripe)
// and carries a glyph so the suggestion list reads as polished rows, not raw
// pills. Clicking a row drops its text straight into the describe box.
const AI_STARTERS = [
  { Icon: Mail, key: "createAI.starterSheetSummary" },
  { Icon: MessageSquare, key: "createAI.starterFormToSlack" },
  { Icon: Sheet, key: "createAI.starterGmailToSheet" },
  { Icon: Bell, key: "createAI.starterStripeFail" },
];

type CreateTab = "ai" | "blank" | "template";

// CreateFlow is the single surface for starting a new flow. Three tabs:
//  - "From a template" (default): the pre-built gallery (TemplateGallery),
//    copied in one click. It leads because it is the only option that hands a
//    beginner a WORKING flow without them designing anything, and the only one
//    with no external dependency. AI used to be the default on the theory that
//    it was the fastest path for a non-technical user — but it needs a
//    connected Claude/OpenAI account, so on a fresh or self-hosted workspace
//    the default tab was a dead end that asked the user to go connect a paid
//    service before they had seen the product do anything.
//  - "AI assisted": describe a flow and let AI draft it.
//  - "Blank": name + description, an empty graph opened in the editor.
// It replaces the old standalone /templates page and the three competing
// create buttons that used to live on the Flows list. The active tab is
// reflected in ?tab=ai|blank|template so the /templates redirect and any
// deep-link can open straight onto the right surface.
export function CreateFlow() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const raw = searchParams.get("tab");
  const tab: CreateTab =
    raw === "blank" ? "blank" : raw === "ai" ? "ai" : "template";
  const setTab = (next: CreateTab) => {
    const sp = new URLSearchParams(searchParams);
    sp.set("tab", next);
    setSearchParams(sp, { replace: true });
  };

  const tabs: { key: CreateTab; Icon: typeof Sparkles; label: string }[] = [
    { key: "template", Icon: LayoutTemplate, label: t("createFlow.tabTemplate") },
    { key: "ai", Icon: Sparkles, label: t("createFlow.tabAI") },
    { key: "blank", Icon: FilePlus2, label: t("createFlow.tabBlank") },
  ];

  return (
    <div className="page">
      <h1>{t("createFlow.title")}</h1>
      <div className="create-flow-tabs" role="tablist">
        {tabs.map(({ key, Icon, label }) => (
          <button
            key={key}
            type="button"
            role="tab"
            aria-selected={tab === key}
            className={"create-flow-tab" + (tab === key ? " active" : "")}
            onClick={() => setTab(key)}
          >
            <Icon size={ICON.md} />
            {label}
          </button>
        ))}
      </div>
      {tab === "template" ? <TemplateGallery /> : <FromScratch mode={tab} />}
    </div>
  );
}

// FromScratch holds the blank-vs-AI creation form, driven by the `mode` the
// parent tab selects. "blank" saves an empty graph and opens the editor; "ai"
// streams a draft from the server (same grounded+validated path the old modal
// used) and opens it for review — nothing is run either way.
function FromScratch({ mode }: { mode: "ai" | "blank" }) {
  const { t, i18n } = useTranslation();
  const { token, me, activeTenant, activeWorkspace, hasPerm } = useAuth();
  const canEdit = hasPerm("graph:edit");
  // Connecting an AI provider needs secret:write. A member who can edit
  // flows but can't connect apps would otherwise be sent to a dead-end
  // ("ask an admin"), so for them we show an informational hint instead of
  // the Connect CTA.
  const canConnect = hasPerm("secret:write");
  const navigate = useNavigate();
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
    const id = `${flowSlug(name)}-${Math.random().toString(36).slice(2, 8)}`;
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
    const id = `${flowSlug(flowName)}-${Math.random().toString(36).slice(2, 8)}`;
    await api.saveGraph(token, {
      id,
      tenant: activeTenant,
      workspace: activeWorkspace,
      nodes: graph.nodes ?? [],
      edges: graph.edges ?? [],
      name: flowName,
      description: graph.description,
    });
    // animateBuild signals the editor to play the build animation on first
    // load (drops appear and wire up in sequence) instead of snapping the
    // finished graph onto the canvas — this is an AI-built flow, so showing
    // it assemble reinforces "the assistant built this for you".
    navigate(`/flows/${encodeURIComponent(id)}`, { state: { animateBuild: true } });
  };

  const submitBlank = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await createNew();
    } catch (e) {
      setErr(explainApiError(e, t));
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
      setErr(explainApiError(e, t));
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
    const summary = flowSummary(pendingDraft.graph, manifests, i18n.language);
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
                <Button type="submit" disabled={refineText.trim() === ""}>
                  <Sparkles size={ICON.sm} />
                  {t("createFlow.refineCta")}
                </Button>
              </div>
            </form>
          )}

          {err && <ErrorNotice>{err}</ErrorNotice>}

          <div className="create-flow-actions">
            <Button disabled={busy} onClick={() => setPendingDraft(null)}>
              {t("common.back")}
            </Button>
            <Button
              variant="primary"
              disabled={busy}
              onClick={() => createFromGraph(pendingDraft.graph)}
            >
              {t("createFlow.openDraft")}
            </Button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="create-flow-scratch">
      {mode === "blank" ? (
        <form className="create-flow-card card" onSubmit={submitBlank}>
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="new-flow-name">{t("common.name")}</label>
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
            <ErrorNotice>{err}</ErrorNotice>
          )}
          <div className="create-flow-actions">
            <Button
              type="submit"
              variant="primary"
              disabled={busy || !name.trim() || !canEdit}
              title={!canEdit ? t("flowList.needEdit") : undefined}
            >
              {busy ? t("flowList.creating") : t("flowList.createCta")}
            </Button>
          </div>
        </form>
      ) : (
        <form className="create-flow-card card" onSubmit={submitAI}>
          <textarea
            id="ai-flow-desc"
            className="ai-create-input"
            autoFocus
            rows={3}
            value={aiDesc}
            placeholder={t("createAI.placeholder")}
            onChange={(e) => setAiDesc(e.target.value)}
          />
          {/* Example prompts: a non-techy user's on-ramp. Shown until they've
              typed something or generation has started; clicking fills the box. */}
          {!busy && steps.length === 0 && aiDesc.trim() === "" && (
            <div className="ai-starters-list">
              {AI_STARTERS.map(({ Icon, key }) => {
                const text = t(key);
                return (
                  <Button
                    key={key}
                    className="ai-starter"
                    onClick={() => setAiDesc(text)}
                  >
                    <span className="ai-starter-icon">
                      <Icon size={ICON.md} strokeWidth={2} />
                    </span>
                    <span className="ai-starter-text">{text}</span>
                  </Button>
                );
              })}
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
          {err && <ErrorNotice>{err}</ErrorNotice>}
          {/* No AI provider connected yet: this is first-run onboarding, not an
              error — show a friendly Connect CTA (same shape as the node
              "Connect X" buttons) in place of the disabled Generate button,
              so there aren't two competing primary actions. */}
          {needConnect &&
            (canConnect ? (
              <p className="ai-connect-hint">{t("createAI.connectHint")}</p>
            ) : (
              // No permission to connect: a warning callout makes the
              // "ask an admin" message register as a blocked state rather
              // than easily-missed muted text.
              <Callout variant="warning">{t("createAI.connectHintNoPerm")}</Callout>
            ))}
          <div className="create-flow-actions">
            {needConnect ? (
              // Only offer the Connect CTA to members who can actually
              // connect (secret:write). Others got the "ask an admin" hint
              // above; sending them to /apps would dead-end on a form they
              // can't use.
              canConnect && (
                <Button
                  variant="primary"
                  className="ai-connect-cta"
                  onClick={() => navigate("/apps?category=ai")}
                >
                  <Sparkles size={ICON.sm} />
                  {t("createAI.connectCta")}
                </Button>
              )
            ) : (
              <Button
                type="submit"
                variant="primary"
                disabled={busy || aiDesc.trim() === "" || !canEdit}
                title={!canEdit ? t("flowList.needEdit") : undefined}
              >
                <Sparkles size={ICON.sm} />
                {busy ? t("createAI.generating") : t("createAI.generate")}
              </Button>
            )}
          </div>
        </form>
      )}
    </div>
  );
}

// flowSummary turns a graph into a plain-language, ordered list of what each
// step does (module id → friendly "Label — subtitle"), so a non-technical user
// sees what was built without reading the node canvas.
function flowSummary(
  graph: Graph,
  manifests: Manifest[],
  lang?: string,
): string[] {
  const byId = new Map(manifests.map((m) => [m.id, m]));
  return (graph.nodes ?? []).map((n) => {
    const m = byId.get(n.module);
    if (!m) return n.module;
    const label = dropLabel(m, lang);
    const sub = dropSubtitle(m, lang);
    return sub ? `${label} — ${sub}` : label;
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

// flowSlug turns a human flow name into the [A-Za-z0-9_.-] machine ID the
// daemon expects: the shared slugify, plus this page's own fallback. A name
// that is all punctuation slugifies to "", which would leave a bare
// "-a1b2c3" id, so it becomes "flow" instead.
function flowSlug(name: string): string {
  return slugify(name) || "flow";
}
