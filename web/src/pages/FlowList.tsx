import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Workflow, Lock, Globe } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import { iconFor, isBrandedIcon } from "../icons";
import type { FlowSummary } from "../types";

export function FlowList() {
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
    const id = window.prompt("New flow ID:");
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
          <h1>Flows</h1>
          <div className="sub">
            {activeTenant || me?.tenant}/{activeWorkspace}
          </div>
        </div>
        <button className="primary" onClick={createNew}>
          <Plus size={16} style={{ marginRight: 6, verticalAlign: -3 }} />
          New flow
        </button>
      </div>
      {loading && <div className="card">Loading…</div>}
      {error && <div className="card" style={{ color: "var(--danger)" }}>{error}</div>}
      {!loading && !error && flows.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          No flows yet. Create one to get started.
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
                          ? "Private — only you can see this flow"
                          : `Private — owned by ${f.owner ?? "(unknown)"}`
                      }
                    >
                      <Lock size={11} />
                      Private
                    </span>
                  ) : (
                    <span className="vis-badge org" title="Visible to everyone in this workspace">
                      <Globe size={11} />
                      Org
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
                      Owner: <code>{f.owner}</code>
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
