// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Link } from "react-router-dom";
import { ArrowLeft } from "lucide-react";
import { ICON } from "../../icons";

/**
 * BackLink is the one way a detail page offers a route back to its parent.
 * The label NAMES THE PARENT ("Organizations"), never "Back": the arrow already
 * says back, and "Organizations, link" reads correctly to a screen reader where
 * "Back, link" is meaningless. It is deliberately not a breadcrumb trail: the
 * app's IA is one level deep almost everywhere, so one hop back is all any
 * surface needs.
 */
export function BackLink({ to, label }: { to: string; label: string }) {
  return (
    <Link to={to} className="back-link">
      <ArrowLeft size={ICON.sm} aria-hidden="true" /> {label}
    </Link>
  );
}
