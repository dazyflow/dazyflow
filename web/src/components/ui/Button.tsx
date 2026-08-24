// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  forwardRef,
  type AnchorHTMLAttributes,
  type ButtonHTMLAttributes,
  type ReactNode,
} from "react";

// Button is the app's single source of truth for clickable actions. Every
// button reads its appearance from *what it does* (the variant), not from a
// hand-picked class at the call site. The vocabulary is deliberately small:
//
//   variant — the action's intent:
//     primary    The one forward/affirmative action on a surface (Save,
//                Create, Connect, Submit). Aim for at most one per view.
//     secondary  The standard, neutral action (Cancel, Back, …). Default.
//     ghost      Low-emphasis: toolbar buttons, inline row actions, dismiss.
//     danger     Destructive / irreversible (Delete, Revoke, Disconnect).
//     warning    Reversible caution / paused state (pause or disable a flow).
//     link       Reads as a text link, sitting in-flow inside prose or a row.
//
//   size — density, not meaning:
//     md         Standard. Default.
//     sm         Compact: dense toolbars, chips, inline rows.
//     icon       Square, icon-only. MUST carry an aria-label (or title).
//
// Modifiers are props, never variants: `icon` (leading glyph), `collapseLabel`
// (icon + label that drops to icon-only on narrow screens), `block`
// (full-width), `loading` (shows busy + disables), `filled` (see below).
//
// `filled` only means anything with variant="danger", where it swaps the
// outlined default for solid red on white. Use it for the moment a
// destructive action IS the affirmative action — the final confirm in a
// dialog, and the Stop button that replaces Run while a run is in flight.
// Those need to hold the same visual weight as the button they stand in for,
// which an outline cannot. Everything else destructive stays outlined.
// It exists as a prop because the editor's Stop and the run-detail page's
// Stop had each grown their own page-scoped CSS override of `.primary` /
// `.danger` instead, so the same action rendered as a red block on one
// surface and a red outline on the other.
//
// ONE deliberate exception, because the variant list can't express it: a quiet
// inline destructive action is `variant="ghost" className="danger"`, which the
// stylesheet defines as its own compound (`.ghost.danger`, app.css) — ghost's
// transparent fill with danger's red ink, and NO border. Plain
// `variant="danger"` is the outlined form and is what a standalone destructive
// button should use. The compound is used by the row-level delete icons in
// Files, CredentialsManager, IconUpload and AdminSecretManager. It looks like a
// call site bypassing the variant API and is not — leaving this note here so
// the next audit doesn't "fix" six correct call sites into growing a border.
//
// Toggle/tab/chip selectors (the `.active` family — theme options, role
// templates, cron chips, segmented tabs) are *selectable state*, not actions,
// and deliberately live outside this component.
//
// The emitted class names mirror the variant/size names so existing contextual
// CSS (`.signin button.primary`, `.confirm-dialog .settings-foot button.danger`,
// …) keeps matching unchanged. A `btn` base class is always present so anchors
// rendered through <ButtonLink> pick up the same chrome as native buttons.

export type ButtonVariant =
  | "primary"
  | "secondary"
  | "ghost"
  | "danger"
  | "warning"
  | "link";

export type ButtonSize = "md" | "sm" | "icon";

interface ButtonBaseProps {
  variant?: ButtonVariant;
  size?: ButtonSize;
  // Leading icon, rendered before the label. Pass an icon element from icons.tsx.
  icon?: ReactNode;
  // Drop the text label on narrow screens, keeping the icon (old .icon-text-btn).
  // Wraps children in a .btn-label span so the media query can hide it.
  collapseLabel?: boolean;
  // Full-width: stretches to the container (CTA under a description, etc.).
  block?: boolean;
  // Busy state: visually disabled and non-interactive while an action runs.
  loading?: boolean;
  // Solid red instead of the outlined default. Only meaningful with
  // variant="danger" — see the note at the top of this file.
  filled?: boolean;
}

// Compose the class list from the semantic props. `secondary`/`md` emit no
// class — they are the bare base look — so the common case stays clean.
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
    // Escape hatch for the rare layout/positioning class (e.g. floating).
    // Variant/size belong in the typed props, not here.
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
        // Default to type="button" so a button inside a <form> doesn't submit
        // it by accident; callers opt into "submit" explicitly.
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
        // withBase: anchors aren't <button> elements, so they need the `.btn`
        // base skin the element selector gives native buttons for free.
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
