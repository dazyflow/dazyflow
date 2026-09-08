// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  forwardRef,
  type AnchorHTMLAttributes,
  type ButtonHTMLAttributes,
  type ReactNode,
} from "react";

// Button is the single component for clickable actions. Appearance follows
// what the action does (variant), never a hand-picked class at the call site.
//
//   variant: primary   the one affirmative action on a surface (at most one per view)
//            secondary neutral (default)
//            ghost     low emphasis: toolbars, inline row actions, dismiss
//            danger    destructive; outlined by default
//            warning   reversible caution (pause/disable)
//            link      reads as a text link inside prose or a row
//   size:    md (default) | sm (dense toolbars, chips) | icon (square; needs aria-label or title)
//
// Modifiers are props: `icon`, `collapseLabel`, `block`, `loading`, `filled`.
// `filled` applies only to danger and makes it solid, for the moment a
// destructive action IS the affirmative one (a dialog's final confirm, the
// Stop that replaces Run). `variant="ghost" className="danger"` is the one
// sanctioned compound (`.ghost.danger` in app.css): a quiet inline destructive
// icon with no border, used by row-level delete icons. It is not a bypass.
//
// Selectable state (the `.active` family: tabs, chips, theme options) is not an
// action and lives outside this component. Emitted class names mirror the
// variant/size names so contextual CSS keeps matching, and `btn` is always
// present so <ButtonLink> anchors share the chrome.

type ButtonVariant =
  | "primary"
  | "secondary"
  | "ghost"
  | "danger"
  | "warning"
  | "link";

type ButtonSize = "md" | "sm" | "icon";

interface ButtonBaseProps {
  variant?: ButtonVariant;
  size?: ButtonSize;
  icon?: ReactNode;
  collapseLabel?: boolean;
  block?: boolean;
  loading?: boolean;
  filled?: boolean;
}

function buttonClasses(
  {
    variant = "secondary",
    size = "md",
    collapseLabel,
    block,
    filled,
  }: ButtonBaseProps,
  extra?: string,
  withBase = false,
): string {
  const parts: string[] = [];
  if (withBase) parts.push("btn");
  if (variant !== "secondary") parts.push(variant);
  if (size !== "md") parts.push(size);
  if (collapseLabel) parts.push("icon-text-btn");
  if (block) parts.push("btn-block");
  if (filled) parts.push("filled");
  if (extra) parts.push(extra);
  return parts.join(" ");
}

// Renders the inner content: optional leading icon, then the label (wrapped in
// .btn-label when it must collapse on mobile).
function ButtonContent({
  icon,
  collapseLabel,
  children,
}: {
  icon?: ReactNode;
  collapseLabel?: boolean;
  children?: ReactNode;
}) {
  return (
    <>
      {icon}
      {collapseLabel ? <span className="btn-label">{children}</span> : children}
    </>
  );
}

type ButtonProps = ButtonBaseProps &
  Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className"> & {
    className?: string;
  };

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    {
      variant,
      size,
      icon,
      collapseLabel,
      block,
      loading,
      filled,
      className,
      children,
      disabled,
      type,
      ...rest
    },
    ref,
  ) {
    if (
      import.meta.env.DEV &&
      size === "icon" &&
      !rest["aria-label"] &&
      !rest.title
    ) {
      console.warn(
        "Button: size='icon' needs an aria-label or title for accessibility.",
      );
    }
    return (
      <button
        ref={ref}
        type={type ?? "button"}
        className={
          buttonClasses(
            { variant, size, collapseLabel, block, filled },
            className,
          ) ||
          undefined
        }
        disabled={disabled || loading}
        aria-busy={loading || undefined}
        {...rest}
      >
        <ButtonContent icon={icon} collapseLabel={collapseLabel}>
          {children}
        </ButtonContent>
      </button>
    );
  },
);

type ButtonLinkProps = ButtonBaseProps &
  Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "className"> & {
    className?: string;
  };

// ButtonLink is a navigational action that must look like a button — an <a>
// styled with the same variants. Use it for hrefs (downloads, external links,
// OAuth start). For in-app routing with react-router, pass the same variant
// classes to <Link>, or wrap as needed.
export const ButtonLink = forwardRef<HTMLAnchorElement, ButtonLinkProps>(
  function ButtonLink(
    {
      variant,
      size,
      icon,
      collapseLabel,
      block,
      filled,
      className,
      children,
      ...rest
    },
    ref,
  ) {
    return (
      <a
        ref={ref}
        className={buttonClasses(
          { variant, size, collapseLabel, block, filled },
          className,
          true,
        )}
        {...rest}
      >
        <ButtonContent icon={icon} collapseLabel={collapseLabel}>
          {children}
        </ButtonContent>
      </a>
    );
  },
);
