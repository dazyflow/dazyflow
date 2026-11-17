// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { type ComponentProps, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "./Button";
import { ICON } from "../../icons";
import { useCopyToClipboard } from "./useCopyToClipboard";

type CopyButtonProps = Omit<ComponentProps<typeof Button>, "onClick" | "children"> & {
  // The text that lands on the clipboard.
  text: string;
  // Resting label; defaults to "Copy". Ignored when size="icon".
  label?: ReactNode;
  // Label while the "copied" tick is showing; defaults to "Copied".
  copiedLabel?: ReactNode;
  iconSize?: number;
  // Drop the Copy/Check glyph for text-only buttons.
  showIcon?: boolean;
};

// CopyButton is the one copy-to-clipboard affordance: Copy glyph + label,
// swapping to Check + "Copied" for FEEDBACK.copied after a successful write.
// Everything else (variant, size, className, style, title) passes through to
// Button. With size="icon" it renders the glyph only and uses the label as
// its accessible name.
export function CopyButton({
  text,
  label,
  copiedLabel,
  iconSize = ICON.xs,
  showIcon = true,
  size,
  title,
  ...rest
}: CopyButtonProps) {
  const { t } = useTranslation();
  const { copied, copy } = useCopyToClipboard();
  const resting = label ?? t("common.copy");
  const done = copiedLabel ?? t("common.copied");
  const shown = copied ? done : resting;
  // Icon-only buttons have no visible label, so it becomes the tooltip and
  // accessible name instead.
  const iconOnly = size === "icon";
  const asString = (n: ReactNode) => (typeof n === "string" ? n : undefined);
  return (
    <Button
      {...rest}
      size={size}
      onClick={() => void copy(text)}
      title={title ?? (iconOnly ? asString(shown) : undefined)}
      aria-label={rest["aria-label"] ?? (iconOnly ? asString(resting) : undefined)}
    >
      {showIcon && (copied ? <Check size={iconSize} /> : <Copy size={iconSize} />)}
      {!iconOnly && shown}
    </Button>
  );
}
