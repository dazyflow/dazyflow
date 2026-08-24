// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import type { SupportBundle } from "../types";

// BundleView renders a redacted SupportBundle attached to a ticket — the
// diagnostic a support agent (or the ticket owner) reads to understand what
// broke, WITHOUT a live read-only grant. It leads with the failing run + error,
// then the flow's steps and any lint/validation issues, and keeps the raw
// redacted JSON behind a disclosure for anyone who wants everything. It never
// contains secrets or run data — the server redacts by construction.
export function BundleView({ bundle }: { bundle: SupportBundle }) {
  const { t } = useTranslation();
  const run = bundle.run;
  const nodeStatus = new Map((run?.nodes ?? []).map((n) => [n.node_id, n]));

  // nodes/edges arrive as null (not []) for a graph with none — Go marshals a
  // nil slice as JSON null. Guard once here rather than at each use.
  const nodes = bundle.nodes ?? [];

  return (
    <div className="bundle-view">
      <div className="bundle-note">
        {t("bundle.redactedNote")}
      </div>

      {/* Run outcome first: it's what a failed ticket is usually about. */}
      {run && (
        <div>
          <div className="bundle-section-head">{t("bundle.runHead")}</div>
          <div>
            <span className={"status-chip " + run.status}>
              <span className={"status-dot " + run.status} /> {run.status}
            </span>
          </div>
          {run.error && (
            <div className="bundle-error">
              {run.error.code && <span className="bundle-error-code">{run.error.code}</span>}
              {run.error.message && <div className="bundle-error-msg">{run.error.message}</div>}
            </div>
          )}
        </div>
      )}

      {/* Steps, with per-node run status when a run is attached. */}
      <div>
        <div className="bundle-section-head">
          {t("bundle.stepsHead")} ({nodes.length})
        </div>
        <div className="bundle-nodes">
          {nodes.map((n) => {
            const nr = nodeStatus.get(n.id);
            return (
              <div key={n.id} className="bundle-node">
                <div className="bundle-node-main">
                  <span className="bundle-node-id">{n.id}</span>
                  <span className="bundle-node-module">{n.module}</span>
                  {n.disabled && <span className="count-pill">{t("bundle.disabled")}</span>}
                  {nr && (
                    <span className={"status-chip " + nr.status}>
                      <span className={"status-dot " + nr.status} /> {nr.status}
                    </span>
                  )}
                </div>
                {nr?.error?.message && <div className="bundle-node-err">{nr.error.message}</div>}
              </div>
            );
          })}
        </div>
      </div>

      {/* Lint / validation issues — safe by design (ids + field names only). */}
      {bundle.issues && bundle.issues.length > 0 && (
        <div>
          <div className="bundle-section-head">
            {t("bundle.issuesHead")} ({bundle.issues.length})
          </div>
          <ul className="bundle-issues">
            {bundle.issues.map((iss, i) => (
              <li key={i}>
                {iss.node_ids && iss.node_ids.length > 0 && <code>{iss.node_ids.join(", ")}</code>}{" "}
                {iss.message || iss.code}
              </li>
            ))}
          </ul>
        </div>
      )}

      <details className="bundle-raw">
        <summary>{t("bundle.rawSummary")}</summary>
        <pre>{JSON.stringify(bundle, null, 2)}</pre>
      </details>
    </div>
  );
}
