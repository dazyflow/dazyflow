// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import type { JobStatus, RunSummary } from "../../types";

const MAX_BARS = 8;

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
      return "var(--faint)";
  }
}

export function RunSparkline({ runs }: { runs: RunSummary[] }) {
  const { t } = useTranslation();
  if (!runs.length) return null;
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
