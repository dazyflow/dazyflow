// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ComponentProps, ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "./Button";
import { ICON } from "../../icons";
import { useCopyToClipboard } from "./useCopyToClipboard";

type CopyButtonProps = Omit<
  ComponentProps<typeof Button>,
  "onClick" | "children" | "icon"
> & {
  // What goes on the clipboard.
  text: string;
  // Resting label; defaults to "Copy". Swapped for copiedLabel (default
  // "Copied") while the tick is showing.
  label?: ReactNode;
  copiedLabel?: ReactNode;
  // size="icon" renders the icon alone; the label becomes the tooltip.
  iconSize?: number;
  showIcon?: boolean;
};

// CopyButton is the one copy-to-clipboard affordance: Copy icon + label,
// flipping to a tick + "Copied" for FEEDBACK.copied after a successful write.
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
  const restLabel = label ?? t("common.copy");
  const doneLabel = copiedLabel ?? t("common.copied");
  const iconOnly = size === "icon";
  const shown = copied ? doneLabel : restLabel;
  return (
    <Button
      {...rest}
      size={size}
      title={
        title ?? (iconOnly && typeof shown === "string" ? shown : undefined)
      }
      aria-label={
        rest["aria-label"] ??
        (iconOnly && typeof restLabel === "string" ? restLabel : undefined)
      }
      icon={
        showIcon ? (
          copied ? (
            <Check size={iconSize} />
          ) : (
            <Copy size={iconSize} />
          )
        ) : undefined
      }
      onClick={() => void copy(text)}
    >
      {iconOnly ? null : shown}
    </Button>
  );
}
