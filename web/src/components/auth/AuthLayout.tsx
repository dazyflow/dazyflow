// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ReactNode } from "react";
import { DOCS, SOURCE, COPYRIGHT_YEAR } from "../../lib/externalLinks";

export function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <div className="signin-wrap">
      <div className="auth-frame">
        {/* A plain anchor, not a router Link: a Link would keep a signed-out
            visitor inside the app, where every unmatched path lands back on
            this page. It points at our OWN root rather than the hardcoded
            dazyflow.app, because on a self-hosted install that constant sent
            the first clickable thing on the first screen to somebody else's
            website. The daemon auth-gates "/" — an anonymous visitor gets the
            marketing landing where one is served, and this page where it
            isn't, which is the right answer in both deployments. */}
        <a className="auth-brand" href="/">
          <img src="/logo.svg" alt="" width={28} height={28} draggable={false} />
          <span className="auth-brand-name">Dazyflow</span>
        </a>

        {children}

        <div className="auth-legal">
          <a className="auth-legal-link" href={DOCS}>
            Docs
          </a>
          <span className="auth-legal-dot" aria-hidden="true">
            ·
          </span>
          <a className="auth-legal-link" href={SOURCE}>
            Source
          </a>
          <span className="auth-legal-dot" aria-hidden="true">
            ·
          </span>
          {/* Same-origin, so they resolve to whatever this deployment serves
              rather than to the hosted product's terms. This is the screen
              where someone types a password and hands over an address; not
              naming the terms that govern that was the gap. */}
          <a className="auth-legal-link" href="/privacy">
            Privacy
          </a>
          <span className="auth-legal-dot" aria-hidden="true">
            ·
          </span>
          <a className="auth-legal-link" href="/terms">
            Terms
          </a>
          <span className="auth-legal-dot" aria-hidden="true">
            ·
          </span>
          {/* Says "Dazyflow", not the operating company. On the marketing site
              the visitor has just come from, Angels' Ware appears nowhere, and
              an unfamiliar name on the password screen reads as a wrong turn.
              The entity still owns the SPDX headers and the legal pages. */}
          <span>© {COPYRIGHT_YEAR} Dazyflow</span>
        </div>
      </div>
    </div>
  );
}
