// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ReactNode } from "react";
import {
  AlertTriangle,
  Info,
  CheckCircle2,
  XCircle,
  LucideIcon,
} from "lucide-react";
import { ICON } from "../../icons";

export type CalloutVariant = "warning" | "info" | "danger" | "success";

const ICONS: Record<CalloutVariant, LucideIcon> = {
  warning: AlertTriangle,
  info: Info,
  danger: XCircle,
  success: CheckCircle2,
};

export function Callout({
  variant = "info",
  children,
  className,
}: {
  variant?: CalloutVariant;
  children: ReactNode;
  className?: string;
}) {
  const Icon = ICONS[variant];
  return (
    <div
      className={`callout callout-${variant}${className ? ` ${className}` : ""}`}
      role="note"
    >
      <Icon size={ICON.md} className="callout-icon" aria-hidden="true" />
      <div className="callout-body">{children}</div>
    </div>
  );
}
