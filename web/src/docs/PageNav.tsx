// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The prev/next pair at the foot of a page.
//
// The guide is written to be read in order — Concepts, then First flow, then
// Connect an app — but until now the only way to move between pages was to go
// back up to the sidebar and find your place in it. This turns the sidebar's
// order into the obvious next click, which is what makes a docs site feel like
// a document rather than a file listing.
import { Link } from "react-router-dom";
import { ArrowLeft, ArrowRight } from "lucide-react";
import { ICON } from "../icons";
import { neighbours } from "./content";

export function PageNav({ path }: { path: string }) {
  const { prev, next } = neighbours(path);
  if (!prev && !next) return null;
  return (
    <nav className="docs-pagenav" aria-label="Page navigation">
      {/* The previous card is rendered even when absent (as an empty span) so
          `next` stays in the right-hand column at the start of the guide, where
          there is no previous page. It carries no `prev` modifier class: the
          card's default layout already IS the previous-page one, and `.next` is
          the only side that overrides it. */}
      {prev ? (
        <Link className="docs-pagenav-link" to={prev.link}>
          <span className="docs-pagenav-dir">
            <ArrowLeft size={ICON.xs} />
            Previous
          </span>
          <span className="docs-pagenav-title">{prev.text}</span>
        </Link>
      ) : (
        <span className="docs-pagenav-gap" />
      )}
      {next && (
        <Link className="docs-pagenav-link next" to={next.link}>
          <span className="docs-pagenav-dir">
            Next
            <ArrowRight size={ICON.xs} />
          </span>
          <span className="docs-pagenav-title">{next.text}</span>
        </Link>
      )}
    </nav>
  );
}
