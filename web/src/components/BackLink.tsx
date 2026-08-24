// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Link } from "react-router-dom";
import { ArrowLeft } from "lucide-react";
import { ICON } from "../icons";

/**
 * BackLink is the one way a detail page offers a route back to its parent.
 *
 * It exists because five hand-rolled copies had drifted apart in two ways
 * that a reader could see:
 *
 *   Styling — two admin pages set className="back-link" AND then re-declared
 *   the same four properties in an inline style, hardcoding `gap: 4` and a
 *   `--space-2` margin against the class's own `var(--space-1)`. So the two
 *   deepest pages in the app sat at a different vertical rhythm from every
 *   other detail page, for no reason anyone had chosen.
 *
 *   Labelling — three conventions at once: name the parent
 *   ("Organizations"), name the action and destination ("Back to runs"), and
 *   a bare "Back" that names nothing.
 *
 * The convention is NAME THE PARENT. The arrow already says "back", so
 * repeating it in the text is redundant, and a bare "Back" throws away the
 * one useful thing the label could carry — where you land. It also reads
 * correctly to a screen reader: "Organizations, link" beats "Back, link",
 * which is meaningless out of context.
 *
 * This deliberately is NOT a breadcrumb trail. The app's IA is one level deep
 * almost everywhere, so a trail would render a redundant `Home > Flows` above
 * a title that already says Flows. Only the operator surfaces go deeper, and
 * one hop back is all any of them needs. See TODO.md for the full reasoning
 * and the condition that would change it.
 */
export function BackLink({ to, label }: { to: string; label: string }) {
  return (
    <Link to={to} className="back-link">
      <ArrowLeft size={ICON.sm} aria-hidden="true" /> {label}
    </Link>
  );
}
