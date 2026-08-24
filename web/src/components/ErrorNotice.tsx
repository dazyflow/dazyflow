// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { CSSProperties, ReactNode } from "react";
import { AlertCircle } from "lucide-react";
import { ICON } from "../icons";

// ErrorNotice is THE way to show a failed action or load in the app UI. It
// exists because error rendering had drifted into three shapes: a bare
// `card error` (small text, no icon), the same class with an AlertCircle
// pasted in by hand, and a plain `card` with an inline danger colour — so the
// identical failure looked different depending on which page you were on.
//
// Everything visual lives in the `.card.error` rule (see app.css), so the icon,
// the danger colour, and the text size stay in lockstep everywhere. Callers
// pass only layout margins via `style`.
export function ErrorNotice({
  children,
  style,
  className,
  action,
}: {
  children: ReactNode;
  style?: CSSProperties;
  className?: string;
  // action is a trailing control (Dismiss, Retry, Sign out) pinned to the end
  // of the row. It sits OUTSIDE the message body so a long message wraps
  // against it rather than pushing it out of the card.
  action?: ReactNode;
}) {
  return (
    // role="alert" so a screen reader announces the failure when it appears —
    // these are almost always rendered in response to a user action.
    <div className={className ? `card error ${className}` : "card error"} style={style} role="alert">
      <AlertCircle size={ICON.sm} aria-hidden="true" />
      {/* A div, not a span: some messages carry block content (a paragraph
          plus a button), which is invalid nested inside a span. */}
      <div className="card-error-body">{children}</div>
      {action}
    </div>
  );
}
