// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { CSSProperties, ReactNode } from "react";

// Notice is the quiet counterpart to ErrorNotice: a short, low-urgency message
// in place of content the user might have expected (loading, nothing here, no
// permission). Everything visual lives in `.card.notice` and `.notice-line`
// (app.css); callers pass only layout margins via `style`.
export function Notice({
  children,
  inline,
  style,
  className,
  role,
}: {
  children: ReactNode;
  // Drops the card chrome for a centred line. Use inside something that
  // already has a border — a dialog body, a panel, a list — where a nested
  // card reads as a box inside a box.
  inline?: boolean;
  style?: CSSProperties;
  className?: string;
  // Passed through rather than fixed, because the right role depends on why
  // the notice appeared: `status` when it reports progress the user is waiting
  // on (see Loading), nothing at all when it is just text in a layout.
  // ErrorNotice can hardcode `alert` because a failure is always worth
  // announcing; a muted line very often is not.
  role?: "status" | "note";
}) {
  const base = inline ? "notice-line" : "card notice";
  return (
    <div className={className ? `${base} ${className}` : base} style={style} role={role}>
      {children}
    </div>
  );
}
