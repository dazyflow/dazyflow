import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Workflow, Lock, Globe } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { iconFor, isBrandedIcon } from "../icons";
import type { FlowSummary } from "../types";

export function FlowList() {
  const { t } = useTranslation();
  const { token, me, activeTenant, activeWorkspace } = useAuth();
  const [flows, setFlows] = useState<FlowSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!token || !me || !activeWorkspace) return;
    let cancelled = false;
    setLoading(true);
    api
      .listGraphs(token, activeTenant, activeWorkspace)
      .then((r) => {
        if (!cancelled) setFlows(r.graphs ?? []);
      })
      .catch((e) => {
        if (!cancelled) setError((e as Error).message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token, me, activeWorkspace]);

  const createNew = async () => {
    if (!token || !me || !activeWorkspace) return;
    const id = window.prompt(t("flowList.newFlowPrompt"));
    if (!id) return;
    await api.saveGraph(token, {
      id,
      tenant: activeTenant,
      workspace: activeWorkspace,
      nodes: [],
      edges: [],
    });
    navigate(`/flows/${encodeURIComponent(id)}`);
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("flowList.title")}</h1>
          <div className="sub">
            {activeTenant || me?.tenant}/{activeWorkspace}
          </div>
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          <Link
            to="/templates"
            style={{ textDecoration: "none" }}
            className="secondary-link"
          >
            <button type="button" className="secondary">
              {t("flowList.fromTemplate")}
            </button>
          </Link>
          <button className="primary" onClick={createNew}>
            <Plus size={16} style={{ marginRight: 6, verticalAlign: -3 }} />
            {t("flowList.newFlow")}
          </button>
        </div>
      </div>
      {loading && <div className="card">{t("common.loading")}</div>}
      {error && <div className="card" style={{ color: "var(--danger)" }}>{error}</div>}
      {!loading && !error && flows.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("flowList.empty")}
        </div>
      )}
      <div className="graph-list">
        {flows.map((f) => {
          const isPrivate = f.visibility === "private";
          const ownedByMe = !!me && f.owner === me.subject;
          const Icon = f.icon ? iconFor(f.icon) : Workflow;
          const displayName = f.name || f.id;
          return (
            <Link
              key={f.id}
              to={`/flows/${encodeURIComponent(f.id)}`}
              style={{ textDecoration: "none", color: "inherit" }}
            >
              <div className="graph-card">
                <div className="name">
                  <Icon
                    size={isBrandedIcon(f.icon) ? 20 : 16}
                    color={isBrandedIcon(f.icon) ? undefined : "currentColor"}
                  />
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span style={{ display: "block" }}>{displayName}</span>
                    {f.name && (
                      <span
                        style={{
                          fontFamily: "var(--font-mono)",
                          fontSize: 11,
                          color: "var(--faint)",
                        }}
                      >
                        {f.id}
                      </span>
                    )}
                  </span>
                  {isPrivate ? (
                    <span
                      className="vis-badge private"
                      title={
                        ownedByMe
                          ? t("flowList.privateOwnedByYou")
                          : t("flowList.privateOwnedBy", {
                              owner: f.owner ?? t("common.unknownParen"),
                            })
                      }
                    >
                      <Lock size={11} />
                      {t("common.private")}
                    </span>
                  ) : (
                    <span
                      className="vis-badge org"
                      title={t("flowList.orgTooltip")}
                    >
                      <Globe size={11} />
                      {t("common.org")}
                    </span>
                  )}
                </div>
                {f.description && (
                  <div
                    className="meta"
                    style={{ color: "var(--muted)", lineHeight: 1.4 }}
                  >
                    {f.description}
                  </div>
                )}
                <div className="meta">
                  {f.owner && (
                    <>
                      {t("flowList.ownerLabel")} <code>{f.owner}</code>
                    </>
                  )}
                </div>
              </div>
            </Link>
          );
        })}
      </div>
    </div>
  );
}
