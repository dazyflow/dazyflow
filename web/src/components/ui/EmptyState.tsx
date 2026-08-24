// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";

// One size for every empty-state glyph. It was 28px on four surfaces and 24px
// on a fifth, which is the kind of difference nobody decides on.
const GLYPH = 28;

// EmptyState is the placeholder for a list, page or panel with nothing in it
// yet: a glyph, a heading, one sentence, and the action that fills it.
//
// Five surfaces built this by hand under four different class families —
// `.admin-empty`, `.flow-empty`, `.trigger-empty`, `.approvals-empty` — and had
// drifted into three separate looks for the same state: a dashed placeholder box
// on the API-keys and users pages, a solid card with a shadow on the flow list
// and the approvals inbox, and a borderless block floating in the triggers
// dialog. The shared look is the dashed one, because a dashed outline says
// "waiting to be filled" and a solid card says "here is some content".
//
// `title` is optional: a no-search-results state has nothing to add above the
// one line explaining itself, and a heading there would just repeat it.
export function EmptyState({
  icon: Icon,
  title,
  children,
  action,
  className,
}: {
  icon: LucideIcon;
  title?: ReactNode;
  // The one sentence. Rendered as a paragraph, so pass text, not blocks.
  children: ReactNode;
  // One button, or several — `.empty-state-actions` lays them out in a row.
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={className ? `empty-state ${className}` : "empty-state"}>
      <Icon size={GLYPH} className="empty-state-icon" aria-hidden="true" />
      {title && <h2 className="empty-state-title">{title}</h2>}
      <p className="empty-state-body">{children}</p>
      {action && <div className="empty-state-actions">{action}</div>}
    </div>
  );
}
