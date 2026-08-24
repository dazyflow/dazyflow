// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { CSSProperties, ReactNode } from "react";

// Notice is the quiet counterpart to ErrorNotice: a short, low-urgency message
// in place of content the user might have expected. A load in progress, a
// nothing-here line, a "you don't have permission to see this" note.
//
// It exists because that shape had been open-coded 42 times as
// `<div className="card" style={{ color: "var(--muted)" }}>`. Nobody chose to
// write it 42 times — each page just needed a muted card and there was nothing
// to reach for, so the padding, the font size and the wrapper element drifted
// apart while the message stayed the same.
//
// Everything visual lives in `.card.notice` and `.notice-line` (see app.css),
// so callers pass only layout margins via `style`.
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
