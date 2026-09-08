// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";

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
  danger?: boolean;
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
