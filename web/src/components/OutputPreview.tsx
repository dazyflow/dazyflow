import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import i18n from "../i18n";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { JobRecord, Ref } from "../types";

// OutputPreview shows what a node emitted on its output ports during the
// most recent run. Fetches /api/v1/me/runs/{run_id}/nodes/{node_id} lazily
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
  const { t } = useTranslation();
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
    return <div style={{ color: "var(--muted)", fontSize: 12 }}>{t("outputPreview.loading")}</div>;
  }
  if (error) {
    return <div style={{ color: "var(--danger)", fontSize: 12 }}>{error}</div>;
  }
  if (!rec) {
    return (
      <div style={{ color: "var(--faint)", fontSize: 12, fontStyle: "italic" }}>
        {t("outputPreview.noOutputYet")}
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
            {t("outputPreview.attempt", { n: rec.Attempt })}
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
          {t("outputPreview.noOutputPorts")}
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
  const { t } = useTranslation();
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
        <pre className="port-value">
          <Linkified text={value} />
        </pre>
      ) : (
        <pre className="port-value">
          <Linkified text={display} />
        </pre>
      )}
      {tooBig && (
        <button
          type="button"
          className="ghost"
          style={{ fontSize: 11, padding: "2px 8px", marginTop: 4 }}
          onClick={() => setExpanded((v) => !v)}
        >
          {expanded ? t("outputPreview.collapse") : t("outputPreview.showAll", { chars: json.length.toLocaleString() })}
        </button>
      )}
    </div>
  );
}

function ErrorBlock({
  error,
}: {
  error: { code: string; message: string; details?: string };
}) {
  const { t } = useTranslation();
  return (
    <div className="port-card port-error">
      <div className="port-head">
        <span className="port-name">{t("outputPreview.errorLabel")}</span>
        <span className="port-mime">{error.code}</span>
      </div>
      <div className="port-error-msg">{error.message}</div>
      {error.details && (
        <details className="port-error-details">
          <summary>{t("outputPreview.errorDetails")}</summary>
          <pre className="port-value">{error.details}</pre>
        </details>
      )}
    </div>
  );
}

// Linkified renders text with any http(s) URLs turned into clickable
// links. Lets a non-technical user tap a step's output URL directly —
// e.g. an ntfy run emits the topic URL (ntfy.sh/<topic>); opening it
// shows the notification and the subscribe button, closing the
// "it said succeeded but my phone is silent" loop. Generic: any step
// that emits a URL gets the same affordance.
function Linkified({ text }: { text: string }) {
  // Stops at quotes/brackets/whitespace so a URL embedded in JSON
  // (`"url": "https://…"`) doesn't swallow the closing quote.
  const re = /(https?:\/\/[^\s"'<>)\]}]+)/g;
  const parts: ReactNode[] = [];
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    parts.push(
      <a key={i++} href={m[0]} target="_blank" rel="noopener noreferrer">
        {m[0]}
      </a>,
    );
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

function formatValue(v: unknown): string {
  if (v === undefined) return i18n.t("outputPreview.emptyValue");
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

// formatRelative produces a short, human-friendly age string. Falls
// back to the raw timestamp on parse failure.
function formatRelative(iso: string): string {
  const ts = Date.parse(iso);
  if (!Number.isFinite(ts)) return iso;
  const diffSec = Math.max(0, Math.round((Date.now() - ts) / 1000));
  if (diffSec < 60) return i18n.t("runList.secondsAgo", { count: diffSec });
  if (diffSec < 3600) return i18n.t("runList.minutesAgo", { count: Math.round(diffSec / 60) });
  if (diffSec < 86400) return i18n.t("runList.hoursAgo", { count: Math.round(diffSec / 3600) });
  return i18n.t("runList.daysAgo", { count: Math.round(diffSec / 86400) });
}
