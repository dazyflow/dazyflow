import { ReactNode } from "react";
import {
  AlertTriangle,
  Info,
  CheckCircle2,
  XCircle,
  LucideIcon,
} from "lucide-react";

// Callout is the shared "this stands out" notice block — a bordered, tinted
// box with a leading icon. Use it instead of a bare muted <p> whenever a
// message needs to register as a state the user should act on or note:
// missing permissions ("ask an admin"), a soft error, a confirmation, a tip.
// One component so every such notice across the app reads the same.
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
      <Icon size={16} className="callout-icon" aria-hidden="true" />
      <div className="callout-body">{children}</div>
    </div>
  );
}
