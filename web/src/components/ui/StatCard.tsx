// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";
import { Link } from "react-router-dom";

export type StatTone = "neutral" | "good" | "warn" | "bad";

export function StatCard({
  icon,
  label,
  value,
  sub,
  tone = "neutral",
  to,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  sub?: ReactNode;
  tone?: StatTone;
  to?: string;
}) {
  const className = "card dash-stat dash-stat-" + tone;
  const body = (
    <>
      <span className="dash-stat-icon">{icon}</span>
      <span className="dash-stat-value">{value}</span>
      <span className="dash-stat-label">{label}</span>
      {sub && <span className="dash-stat-sub">{sub}</span>}
    </>
  );
  return to ? (
    <Link to={to} className={className}>
      {body}
    </Link>
  ) : (
    <div className={className}>{body}</div>
  );
}
