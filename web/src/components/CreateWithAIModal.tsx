import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Sparkles } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import type { Graph } from "../types";

// CreateWithAIModal: describe a flow in plain English → the server generates
// a DRAFT flow graph (grounded + validated). On success the parent saves it
// and opens the editor for review — nothing is run. Shows remaining lint
// findings as a heads-up so the user knows what to check.
export function CreateWithAIModal({
  onCancel,
  onGenerated,
}: {
  onCancel: () => void;
  // Parent persists the draft + navigates to the editor. Returns once done so
  // the modal can keep its busy state until the editor takes over.
  onGenerated: (graph: Graph) => Promise<void>;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [desc, setDesc] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [needConnect, setNeedConnect] = useState(false);
  // Live progress frames streamed from the server (understanding → drafting →
  // validating → repairing). The last one is the active step.
  const [steps, setSteps] = useState<{ phase: string; message: string }[]>([]);
  const [providers, setProviders] = useState<{ name: string; label: string }[]>([]);
  const [provider, setProvider] = useState(
    () => localStorage.getItem("dazyflow.aiProvider") ?? "",
  );

  useEffect(() => {
    if (!token) return;
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
        if (list.length === 0) setNeedConnect(true);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [token]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || desc.trim() === "" || busy) return;
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
        { description: desc.trim(), provider: provider || undefined, tz },
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
        // Hand the draft to the parent (save + open editor). Keep busy — the
        // page is about to navigate away.
        await onGenerated(resultGraph);
        return;
      }
      if (!hadError) setErr(t("createAI.empty"));
    } catch (e) {
      setErr((e as Error).message);
    }
    setBusy(false);
  };

  return (
    <div className="settings-backdrop" onClick={onCancel}>
      <form
        className="settings-dialog"
        style={{ maxWidth: 520 }}
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
      >
        <div className="settings-head">
          <h2>
            <Sparkles size={16} style={{ marginRight: 8, verticalAlign: -2 }} />
            {t("createAI.title")}
          </h2>
        </div>
        <div className="settings-body">
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="ai-flow-desc">{t("createAI.label")}</label>
            </div>
            <textarea
              id="ai-flow-desc"
              autoFocus
              rows={4}
              value={desc}
              placeholder={t("createAI.placeholder")}
              onChange={(e) => setDesc(e.target.value)}
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
        </div>
        <div className="settings-foot">
          <button type="button" onClick={onCancel}>
            {t("common.cancel")}
          </button>
          <button
            type="submit"
            className="primary"
            disabled={busy || desc.trim() === "" || needConnect}
          >
            <Sparkles size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
            {busy ? t("createAI.generating") : t("createAI.generate")}
          </button>
        </div>
      </form>
    </div>
  );
}
