import { useEffect, useState } from "react";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { JobRecord, Ref } from "../types";

// OutputPreview shows what a node emitted on its output ports during the
// most recent run. Fetches /api/v1/jobs/{runID}/nodes/{nodeID} lazily
// when the inspector selection changes. Renders one card per port with
// MIME + a JSON-formatted value (truncated if huge).
type Props = {
  runID: string;
  nodeID: string;
  // refreshKey changes whenever the parent learns this node's status
  // has transitioned. Used as a useEffect dep so the preview refetches
  // mid-run without the operator having to re-select the node.
  refreshKey?: number;
};

export function OutputPreview({ runID, nodeID, refreshKey }: Props) {
  const { token } = useAuth();
  const [rec, setRec] = useState<JobRecord | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    api
      .getNodeRecord(token, runID, nodeID)
      .then((r) => {
        if (!cancelled) setRec(r);
      })
      .catch((e: APIError | Error) => {
        if (!cancelled) {
          // 404 just means "this node hasn't run yet in this run" —
          // show a calm empty state, not a red error.
          if (e instanceof APIError && e.status === 404) {
            setRec(null);
            setError(null);
          } else {
            setError(e.message);
          }
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token, runID, nodeID, refreshKey]);

  if (loading) {
    return <div style={{ color: "var(--muted)", fontSize: 12 }}>Loading…</div>;
  }
  if (error) {
    return <div style={{ color: "var(--danger)", fontSize: 12 }}>{error}</div>;
  }
  if (!rec) {
    return (
      <div style={{ color: "var(--faint)", fontSize: 12, fontStyle: "italic" }}>
        No run output for this node yet. Click Run to execute.
      </div>
    );
  }
  return (
    <div className="output-preview">
      <div className="output-row">
        <span className={"status-dot " + rec.Status} />
        <span style={{ fontSize: 13, fontWeight: 500 }}>{rec.Status}</span>
        {rec.Attempt && rec.Attempt > 1 && (
          <span style={{ fontSize: 11, color: "var(--faint)" }}>
            attempt {rec.Attempt}
          </span>
        )}
        {rec.FinishedAt && (
          <span style={{ fontSize: 11, color: "var(--faint)", marginLeft: "auto" }}>
            {formatRelative(rec.FinishedAt)}
          </span>
        )}
      </div>
      {rec.Result?.error && <ErrorBlock error={rec.Result.error} />}
      {rec.Result?.output && Object.keys(rec.Result.output).length > 0 && (
        <PortList output={rec.Result.output} />
      )}
      {rec.Result?.output && Object.keys(rec.Result.output).length === 0 && !rec.Result.error && (
        <div style={{ fontSize: 12, color: "var(--faint)", fontStyle: "italic" }}>
          (no output ports emitted)
        </div>
      )}
    </div>
  );
}

function PortList({ output }: { output: Record<string, Ref> }) {
  const entries = Object.entries(output);
  return (
    <div className="port-list">
      {entries.map(([port, ref]) => (
        <PortCard key={port} port={port} ref0={ref} />
      ))}
    </div>
  );
}

function PortCard({ port, ref0 }: { port: string; ref0: Ref }) {
  const [expanded, setExpanded] = useState(false);
  const value = ref0.data;
  const isPrimitive =
    value === null ||
    typeof value === "string" ||
    typeof value === "number" ||
    typeof value === "boolean";
  // For very large payloads, collapse by default and offer expand.
  const json = formatValue(value);
  const truncatedThreshold = 800;
  const tooBig = json.length > truncatedThreshold;
  const display = expanded || !tooBig ? json : json.slice(0, truncatedThreshold) + "\n…";

  return (
    <div className="port-card">
      <div className="port-head">
        <span className="port-name">{port}</span>
        {ref0.mime && <span className="port-mime">{ref0.mime}</span>}
      </div>
      {isPrimitive && typeof value === "string" ? (
        <pre className="port-value">{value}</pre>
      ) : (
        <pre className="port-value">{display}</pre>
      )}
      {tooBig && (
        <button
          type="button"
          className="ghost"
          style={{ fontSize: 11, padding: "2px 8px", marginTop: 4 }}
          onClick={() => setExpanded((v) => !v)}
        >
          {expanded ? "Collapse" : `Show all (${json.length.toLocaleString()} chars)`}
        </button>
      )}
    </div>
  );
}

function ErrorBlock({ error }: { error: { code: string; message: string } }) {
  return (
    <div className="port-card port-error">
      <div className="port-head">
        <span className="port-name">error</span>
        <span className="port-mime">{error.code}</span>
      </div>
      <pre className="port-value">{error.message}</pre>
    </div>
  );
}

function formatValue(v: unknown): string {
  if (v === undefined) return "(empty)";
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

// formatRelative produces a short, human-friendly age string like "2s
// ago" or "5m ago". Falls back to the raw timestamp on parse failure.
function formatRelative(iso: string): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return iso;
  const diffSec = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (diffSec < 60) return `${diffSec}s ago`;
  if (diffSec < 3600) return `${Math.round(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.round(diffSec / 3600)}h ago`;
  return `${Math.round(diffSec / 86400)}d ago`;
}
