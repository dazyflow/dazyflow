// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Zap, MousePointerClick, PauseCircle, UploadCloud } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { FlowRunStatus } from "../flowStatus";

// FlowStatusChip is the at-a-glance "will this run on its own?" badge.
// Three states (live / manual / paused) computed by flowRunStatus (editor,
// Triggers modal) or the server's run_status (flow list). Colour + icon
// carry the meaning; the tooltip explains it so nobody has to guess again
// whether a saved flow is actually scheduled.
const ICONS: Record<FlowRunStatus, typeof Zap> = {
  live: Zap,
  manual: MousePointerClick,
  paused: PauseCircle,
  needs_publish: UploadCloud,
};

export function FlowStatusChip({
  status,
  size = "md",
}: {
  status: FlowRunStatus;
  size?: "sm" | "md";
}) {
  const { t } = useTranslation();
  const Icon = ICONS[status];
  return (
    <span
      className={`flow-status-chip flow-status-${status} flow-status-${size}`}
      title={t(`flowStatus.${status}.tip`)}
      role="status"
    >
      <Icon size={size === "sm" ? 12 : 14} aria-hidden />
      <span className="flow-status-chip-label">
        {t(`flowStatus.${status}.label`)}
      </span>
    </span>
  );
}
