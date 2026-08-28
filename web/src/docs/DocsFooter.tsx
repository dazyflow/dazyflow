// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The site footer, under the prev/next pair at the foot of every page.
//
// A reference site ends somewhere, and until now this one just stopped: the
// last thing on the page was whatever the last table happened to be. That
// reads as an internal build rather than a product's documentation, and it
// leaves the two questions a reader most often has at the bottom of a docs
// page — what is this thing, and where do I get it — with nowhere to land.
//
// The link columns are DERIVED FROM NAV, the same decision ORDER makes in
// content.ts: a footer that lists its own copy of the sidebar is a second
// list to keep in step, and the failure mode is a footer quietly pointing at
// a page that moved. Only the off-site links are written out here, because
// nothing in the nav knows about them.
import { Link } from "react-router-dom";
import { NAV } from "./content";
import { SITE, SOURCE, LICENSE, CONTACT, INVITE } from "./links";

// The guide, split at the point where it stops being a walkthrough and starts
// being reference. slice() rather than two hand-written lists so a page added
// to the sidebar lands in a column instead of going missing.
const GUIDE = NAV[0].items.slice(0, 5);
const REFERENCE = [...NAV[0].items.slice(5), ...NAV[1].items.slice(0, 1)];

const YEAR = 2026;

export function DocsFooter() {
  return (
    <footer className="docs-footer">
      <div className="docs-footer-main">
        <div className="docs-footer-brand">
          <span className="docs-footer-mark">
            <img src="/logo.svg" alt="" width={22} height={22} draggable={false} />
            <span className="docs-footer-name">Dazyflow</span>
          </span>
          <p className="docs-footer-blurb">
            Build automations without code — connect your apps and let the repetitive work
            run itself.
          </p>
        </div>

        <nav className="docs-footer-cols" aria-label="Footer">
          <div className="docs-footer-col">
            <h2 className="docs-footer-heading">Guide</h2>
            {GUIDE.map((i) => (
              <Link key={i.link} className="docs-footer-link" to={i.link}>
                {i.text}
              </Link>
            ))}
          </div>

          <div className="docs-footer-col">
            <h2 className="docs-footer-heading">Reference</h2>
            {REFERENCE.map((i) => (
              <Link key={i.link} className="docs-footer-link" to={i.link}>
                {i.text}
              </Link>
            ))}
          </div>

          <div className="docs-footer-col">
            <h2 className="docs-footer-heading">Project</h2>
            {/* Off-site, so plain anchors with the same treatment the Markdown
                renderer gives an external link. */}
            <a className="docs-footer-link" href={SITE}>
              Product site
            </a>
            <a className="docs-footer-link" href={SOURCE}>
              Source code
            </a>
            <a className="docs-footer-link" href={LICENSE}>
              License
            </a>
            <a className="docs-footer-link" href={CONTACT}>
              Contact
            </a>
            <a className="docs-footer-link" href={INVITE}>
              Request an invite
            </a>
          </div>
        </nav>
      </div>

      <div className="docs-footer-legal">
        <span>© {YEAR} Joachim Klahr</span>
        {/* Named in full rather than as a bare "AGPL" badge: the licence is the
            reason self-hosting is an option at all, and the version is the part
            that carries the obligation. */}
        <span>
          Free software under the{" "}
          <a className="docs-footer-legal-link" href={LICENSE}>
            GNU AGPL v3
          </a>
        </span>
      </div>
    </footer>
  );
}
