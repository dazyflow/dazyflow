// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { CSSProperties, ReactNode } from "react";

export function Notice({
  children,
  inline,
  style,
  className,
  role,
}: {
  children: ReactNode;
  inline?: boolean;
  style?: CSSProperties;
  className?: string;
  role?: "status" | "note";
}) {
  const base = inline ? "notice-line" : "card notice";
  return (
    <div className={className ? `${base} ${className}` : base} style={style} role={role}>
      {children}
    </div>
  );
}
