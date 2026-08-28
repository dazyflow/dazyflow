// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";

// ActionsCard frames the moderation actions on the user/org detail pages
// as a titled list. Each ActionRow pairs an explanation (icon + title +
// one-line description) with its button, so a destructive action always
// states what it does right next to the control — far clearer than a row
// of unlabelled buttons.
export function ActionsCard({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="pa-actions">
      <div className="pa-actions-head">{title}</div>
      {children}
    </div>
  );
}

export function ActionRow({
  icon,
  title,
  description,
  danger,
  children,
}: {
  icon: ReactNode;
  title: string;
  description: string;
  // danger tints the icon to signal an irreversible / destructive action.
  danger?: boolean;
  // children is the action control (a Button).
  children: ReactNode;
}) {
  return (
    <div className={"pa-action-row" + (danger ? " is-danger" : "")}>
      <div className="pa-action-info">
        {icon}
        <div className="pa-action-text">
          <div className="pa-action-title">{title}</div>
          <div className="pa-action-desc">{description}</div>
        </div>
      </div>
      {children}
    </div>
  );
}
