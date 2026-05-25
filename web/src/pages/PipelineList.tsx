import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Workflow } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";

export function PipelineList() {
  const { token, me } = useAuth();
  const [graphs, setGraphs] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const navigate = useNavigate();

  useEffect(() => {
    if (!token || !me) return;
    let cancelled = false;
    api
      .listGraphs(token, me.tenant, me.workspace)
      .then((r) => {
        if (!cancelled) setGraphs(r.graphs ?? []);
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
  }, [token, me]);

  const createNew = async () => {
    if (!token || !me) return;
    const id = window.prompt("New pipeline ID:");
    if (!id) return;
    await api.saveGraph(token, {
      id,
      tenant: me.tenant,
      workspace: me.workspace,
      nodes: [],
      edges: [],
    });
    navigate(`/pipelines/${encodeURIComponent(id)}`);
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>Pipelines</h1>
          <div className="sub">
            {me?.tenant}/{me?.workspace}
          </div>
        </div>
        <button className="primary" onClick={createNew}>
          <Plus size={16} style={{ marginRight: 6, verticalAlign: -3 }} />
          New pipeline
        </button>
      </div>
      {loading && <div className="card">Loading…</div>}
      {error && <div className="card" style={{ color: "var(--danger)" }}>{error}</div>}
      {!loading && !error && graphs.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          No pipelines yet. Create one to get started.
        </div>
      )}
      <div className="graph-list">
        {graphs.map((id) => (
          <Link
            key={id}
            to={`/pipelines/${encodeURIComponent(id)}`}
            style={{ textDecoration: "none", color: "inherit" }}
          >
            <div className="graph-card">
              <div className="name">
                <Workflow size={16} />
                {id}
              </div>
              <div className="meta">Click to edit</div>
            </div>
          </Link>
        ))}
      </div>
    </div>
  );
}
