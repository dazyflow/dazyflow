// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";

const GLYPH = 28;

// EmptyState is the placeholder for a list, page or panel with nothing in it
// yet: a glyph, a heading, one sentence, and the action that fills it.
//
// Five surfaces built this by hand under four class families and had drifted
// into three looks for the same state. The shared look is the dashed one,
// because a dashed outline says "waiting to be filled" where a solid card says
// "here is some content".
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
  children: ReactNode;
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
