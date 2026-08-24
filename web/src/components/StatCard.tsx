// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";
import { Link } from "react-router-dom";

// StatCard is the icon + number + label tile used on the dashboard, the
// plan/usage page and the TV overview's siblings. `tone` colours it by what the
// number means, not by which page it's on.
//
// Dashboard and Usage each had their own copy of this, rendering the identical
// markup and the identical `card dash-stat dash-stat-{tone}` classes. Usage's
// was the strict superset — it allowed a tile with no destination — so that is
// the one that survived; the Dashboard version only ever differed by requiring
// `to` and narrowing `sub` to a string.
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
  // Omit for a tile that only reports — it renders a plain div rather than a
  // link, so there's no affordance suggesting a destination that isn't there.
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
