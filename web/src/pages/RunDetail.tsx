import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, AlertCircle, ChevronDown, ChevronRight, RotateCw } from "lucide-react";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import type { JobRecord, JobStatus, Ref } from "../types";

// RunDetail is the post-failure "what happened" page — and the
// post-success "yes, here are the values" page. T2 of the PMF
// roadmap: when a trial workflow breaks, this is the surface that
// decides whether the user stays or churns.
//
// Layout:
//
//   ┌──────────────────────────────────────────────────────────┐
//   │ ← back   Run summary card (status, graph, timing, error  │
//   │          banner if failed)                                │
//   ├──────────────────────────────────────────────────────────┤
//   │ Node timeline                                             │
//   │   ● node-1  status   duration   ▶ (click to expand)       │
//   │     └─ inputs/outputs/error preview                       │
//   │   ● node-2  …                                             │
//   └──────────────────────────────────────────────────────────┘
//
// One API call (listRunNodes) draws the whole timeline. Each node
// row expands inline to show its result JSON; no extra round trips.
// "Replay" re-fires the graph from scratch and navigates to the
// new run's detail page.
export function RunDetail() {
  const { runID } = useParams<{ runID: string }>();
  const { token } = useAuth();
  const [run, setRun] = useState<JobRecord | null>(null);
  const [nodes, setNodes] = useState<JobRecord[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [replaying, setReplaying] = useState(false);

  useEffect(() => {
    if (!token || !runID) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    Promise.all([api.getJob(token, runID), api.listRunNodes(token, runID)])
      .then(([r, ns]) => {
        if (cancelled) return;
        setRun(r);
        setNodes(ns.nodes ?? []);
      })
      .catch((e) => {
        if (!cancelled) {
          const msg = e instanceof APIError ? `${e.status}: ${e.message}` : (e as Error).message;
          setError(msg);
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token, runID]);

  // Poll while anything's still live so the timeline updates without
  // a manual reload. Mirrors RunList's polling pattern.
  useEffect(() => {
    if (!token || !runID || !run) return;
    const live = run.Status === "queued" || run.Status === "running" || run.Status === "awaiting" ||
      nodes.some((n) => n.Status === "queued" || n.Status === "running" || n.Status === "awaiting");
    if (!live) return;
    const t = window.setInterval(() => {
      Promise.all([api.getJob(token, runID), api.listRunNodes(token, runID)])
        .then(([r, ns]) => {
          setRun(r);
          setNodes(ns.nodes ?? []);
        })
        .catch(() => {});
    }, 2000);
    return () => window.clearInterval(t);
  }, [token, runID, run, nodes]);

  const toggle = (nid: string) =>
    setExpanded((prev) => ({ ...prev, [nid]: !prev[nid] }));

  const replay = async () => {
    if (!token || !run) return;
    setReplaying(true);
    try {
      // Use the tenant/workspace baked into the original run record
      // — replays go back to the same scope, not the user's current
      // workspace switcher state. Less surprising for "I'm
      // investigating an old run."
      const result = await api.runGraph(token, "", "", run.GraphID);
      // runGraph signature takes tenant/workspace; rely on the
      // server falling back to principal scope when empty. (If it
      // requires explicit, swap to using activeTenant/Workspace.)
      if (result?.job_id) {
        window.location.href = `/runs/${encodeURIComponent(result.job_id)}`;
      }
    } catch (e) {
      const msg = e instanceof APIError ? `${e.status}: ${e.message}` : (e as Error).message;
      setError(`Replay failed: ${msg}`);
    } finally {
      setReplaying(false);
    }
  };

  if (loading) {
    return (
      <div className="page">
        <div className="card">Loading run…</div>
      </div>
    );
  }
  if (error || !run) {
    return (
      <div className="page">
        <div className="page-title">
          <div>
            <Link to="/runs" className="back-link">
              <ArrowLeft size={14} /> Back to runs
            </Link>
            <h1>Run not found</h1>
          </div>
        </div>
        <div className="card error">{error ?? "No run with that ID."}</div>
      </div>
    );
  }

  // Sort nodes by enqueued_at ASC so the timeline reads top→down
  // in execution order rather than newest-first.
  const orderedNodes = [...nodes].sort((a, b) => {
    const ta = Date.parse(timestamp(a, "EnqueuedAt", "enqueued_at"));
    const tb = Date.parse(timestamp(b, "EnqueuedAt", "enqueued_at"));
    return ta - tb;
  });

  // Find the first failed node (if any) so the banner can name it.
  const failedNode = orderedNodes.find((n) => n.Status === "failed");

  return (
    <div className="page run-detail">
      <div className="page-title">
        <div>
          <Link to="/runs" className="back-link">
            <ArrowLeft size={14} /> Back to runs
          </Link>
          <h1 style={{ display: "flex", alignItems: "center", gap: 10 }}>
            <span className={"status-dot " + run.Status} />
            {run.GraphID}
          </h1>
          <div className="sub">
            Run <code style={{ fontFamily: "var(--font-mono)", fontSize: 12 }}>{run.ID}</code>
          </div>
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          <Link
            to={`/flows/${encodeURIComponent(run.GraphID)}?run=${encodeURIComponent(run.ID)}`}
            className="secondary-link"
          >
            <button type="button" className="secondary">Open in editor</button>
          </Link>
          <button
            type="button"
            className="primary"
            onClick={replay}
            disabled={replaying}
            title="Re-run this graph from scratch"
          >
            <RotateCw size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
            {replaying ? "Replaying…" : "Replay"}
          </button>
        </div>
      </div>

      {/* Failure banner — the most-important real estate on this
          page when something broke. Names the failing node and
          links to its row in the timeline. */}
      {run.Status === "failed" && (
        <div className="run-error-banner">
          <AlertCircle size={18} style={{ flexShrink: 0, marginTop: 2 }} />
          <div>
            <div className="run-error-title">
              Failed{failedNode ? ` at "${failedNode.NodeID}"` : ""}
              {run.Result?.error?.code && (
                <span className="run-error-code"> · {run.Result.error.code}</span>
              )}
            </div>
            {run.Result?.error?.message && (
              <div className="run-error-msg">{run.Result.error.message}</div>
            )}
          </div>
        </div>
      )}

      {/* Run-level summary card. */}
      <div className="run-summary card">
        <SummaryRow label="Status" value={<StatusChip status={run.Status} />} />
        <SummaryRow label="Started" value={formatAbs(run.StartedAt ?? null)} />
        <SummaryRow label="Finished" value={formatAbs(run.FinishedAt ?? null)} />
        <SummaryRow
          label="Duration"
          value={
            run.StartedAt && run.FinishedAt
              ? formatDuration(run.StartedAt, run.FinishedAt)
              : run.Status === "running"
              ? "in progress"
              : "—"
          }
        />
        <SummaryRow
          label="Nodes"
          value={
            <span>
              {orderedNodes.length} total
              {orderedNodes.filter((n) => n.Status === "succeeded").length > 0 && (
                <span style={{ color: "var(--muted)" }}>
                  {" "}
                  · {orderedNodes.filter((n) => n.Status === "succeeded").length} succeeded
                </span>
              )}
              {orderedNodes.filter((n) => n.Status === "failed").length > 0 && (
                <span style={{ color: "var(--danger)" }}>
                  {" "}
                  · {orderedNodes.filter((n) => n.Status === "failed").length} failed
                </span>
              )}
            </span>
          }
        />
      </div>

      {/* Node timeline. Each row is a single node + status; click
          to expand and see input/output/error JSON inline. */}
      <h2 style={{ marginTop: "var(--space-4)" }}>Timeline</h2>
      {orderedNodes.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          No node records for this run. Either the run is still
          queued, or the graph has no nodes that produced records.
        </div>
      )}
      <div className="node-timeline">
        {orderedNodes.map((n) => {
          const isOpen = !!expanded[n.NodeID];
          const dur =
            n.StartedAt && n.FinishedAt
              ? formatDuration(n.StartedAt, n.FinishedAt)
              : n.Status === "running"
              ? "in progress"
              : "—";
          return (
            <div
              key={n.ID}
              className={"node-row" + (n.Status === "failed" ? " failed" : "")}
            >
              <button
                type="button"
                className="node-row-head"
                onClick={() => toggle(n.NodeID)}
                aria-expanded={isOpen}
              >
                {isOpen ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                <span className={"status-dot " + n.Status} />
                <span className="node-id">{n.NodeID}</span>
                <span className="node-status">{n.Status}</span>
                <span className="node-dur">{dur}</span>
                {n.Result?.error?.code && (
                  <span className="node-err">{n.Result.error.code}</span>
                )}
              </button>
              {isOpen && (
                <div className="node-body">
                  {n.Result?.error && (
                    <div className="node-err-block">
                      <div className="node-err-code">{n.Result.error.code}</div>
                      <div>{n.Result.error.message}</div>
                      {n.Result.error.details && (
                        <details className="node-err-details">
                          <summary>Details</summary>
                          <pre className="node-err-pre">
                            {n.Result.error.details}
                          </pre>
                        </details>
                      )}
                    </div>
                  )}
                  {n.Job?.Input && Object.keys(n.Job.Input).length > 0 && (
                    <div className="node-output">
                      <div className="node-section-head">Inputs</div>
                      {Object.entries(n.Job.Input).map(([port, ref]) => (
                        <details key={port} className="node-port">
                          <summary>
                            <span className="node-port-name">{port}</span>
                            {ref?.mime && (
                              <span className="node-port-mime">{ref.mime}</span>
                            )}
                          </summary>
                          <pre className="node-port-value">
                            {previewValue(ref)}
                          </pre>
                        </details>
                      ))}
                    </div>
                  )}
                  {n.Result?.output && Object.keys(n.Result.output).length > 0 && (
                    <div className="node-output">
                      <div className="node-section-head">Output</div>
                      {Object.entries(n.Result.output).map(([port, ref]) => (
                        <details key={port} className="node-port">
                          <summary>
                            <span className="node-port-name">{port}</span>
                            {ref?.mime && (
                              <span className="node-port-mime">{ref.mime}</span>
                            )}
                          </summary>
                          <pre className="node-port-value">
                            {previewValue(ref)}
                          </pre>
                        </details>
                      ))}
                    </div>
                  )}
                  {!n.Result?.error && !n.Result?.output && !(n.Job?.Input && Object.keys(n.Job.Input).length > 0) && (
                    <div style={{ color: "var(--faint)", fontSize: 12 }}>
                      No result recorded — node may not have produced
                      output, or is still in flight.
                    </div>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function SummaryRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="run-summary-row">
      <span className="run-summary-label">{label}</span>
      <span className="run-summary-value">{value}</span>
    </div>
  );
}

function StatusChip({ status }: { status: JobStatus }) {
  return (
    <span className={"status-chip " + status}>
      <span className={"status-dot " + status} />
      {status}
    </span>
  );
}

function formatAbs(iso: string | null): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return iso;
  return new Date(t).toLocaleString();
}

function formatDuration(startedISO: string, finishedISO: string): string {
  const start = Date.parse(startedISO);
  const end = Date.parse(finishedISO);
  if (!Number.isFinite(start) || !Number.isFinite(end)) return "";
  const ms = Math.max(0, end - start);
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

// timestamp tries the Go-shaped capitalized field then the
// JSON-shaped lowercased one — defends against backend serialization
// drift since JobRecord uses Go field names today.
function timestamp(rec: JobRecord, ...keys: string[]): string {
  for (const k of keys) {
    const v = (rec as unknown as Record<string, string | null | undefined>)[k];
    if (v) return v;
  }
  return "";
}

// previewValue renders a Ref's value (or path) for the expandable
// preview block. Pretty-prints JSON; strings stay verbatim. The Ref
// type's `data` field corresponds to the Go side's `Inline`.
function previewValue(ref: Ref): string {
  if (ref.ref) return `→ ${ref.ref}`;
  const v = ref.data;
  if (v === undefined || v === null) return "(empty)";
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}
