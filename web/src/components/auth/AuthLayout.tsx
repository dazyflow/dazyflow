// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The frame around every signed-out screen: sign in, sign up, forgot/reset
// password, accept invite, verify email.
//
// What it exists to fix: the card said "Sign in" and nothing else. Nothing on
// the page named the product, so someone following a link into
// dazyflow.app/signin — a password reset, an invite, a mistyped URL, which is
// also routed here — met an unbranded box and had to infer where they were.
// The org-branded variant inside SignIn only fires on a wildcard-subdomain
// deploy that has an icon set, so the hosted product's own front door was the
// one with no name on it.
//
// The mark sits OUTSIDE the card on purpose. Inside, it competes with the
// form's own heading and pushes the first field down; above it, it reads as
// the letterhead it is and the card stays a form.
import { ReactNode } from "react";
import { DOCS, SITE, SOURCE, COPYRIGHT_YEAR } from "../../lib/externalLinks";

export function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <div className="signin-wrap">
      <div className="auth-frame">
        {/* Leaves the SPA for the product site, so a plain anchor. A router
            Link would keep a signed-out visitor inside the app, where every
            unmatched path lands back on this page. */}
        <a className="auth-brand" href={SITE}>
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
          <span>© {COPYRIGHT_YEAR} Angels' Ware</span>
        </div>
      </div>
    </div>
  );
}
