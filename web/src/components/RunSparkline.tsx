// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import type { JobStatus, RunSummary } from "../types";

// RunSparkline is the compact recent-run health strip on a flow card: one
// small bar per recent run, oldest on the left, newest on the right, colored
// by outcome. It's the at-a-glance "is this flow healthy?" signal pro
// automation tools show next to each workflow. Purely presentational —
// the caller passes the runs (newest-first, as the API returns them).
const MAX_BARS = 8;

// Map a run status to the same semantic palette the status chips use, so a
// green/red bar here reads identically to a green/red chip elsewhere.
function barColor(status: JobStatus): string {
  switch (status) {
    case "succeeded":
      return "var(--status-completed)";
    case "failed":
      return "var(--status-failed)";
    case "running":
    case "queued":
      return "var(--status-running)";
    case "awaiting":
      return "var(--status-awaiting)";
    default:
      // cancelled / skipped — present but not noteworthy
      return "var(--faint)";
  }
}

export function RunSparkline({ runs }: { runs: RunSummary[] }) {
  const { t } = useTranslation();
  if (!runs.length) return null;
  // Newest-first in; render oldest→newest so the most recent run sits on the
  // right, matching how a timeline reads.
  const recent = runs.slice(0, MAX_BARS).reverse();
  const ok = runs
    .slice(0, MAX_BARS)
    .filter((r) => r.status === "succeeded").length;
  const total = Math.min(runs.length, MAX_BARS);
  return (
    <span
      className="run-spark"
      role="img"
      aria-label={t("flowList.sparkSummary", { ok, total })}
      title={t("flowList.sparkSummary", { ok, total })}
    >
      {recent.map((r) => (
        <span
          key={r.id}
          className="run-spark-bar"
          style={{ background: barColor(r.status) }}
        />
      ))}
    </span>
  );
}
