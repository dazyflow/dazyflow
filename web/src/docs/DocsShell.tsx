// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The docs shell REUSES the app's chrome: it renders the exact same markup and
// class names as web/src/components/AppShell.tsx (.app-shell / .topbar /
// .body / .sidebar / .sidebar-scrim / .main + the burger), so app.css styles it
// identically — same top bar, same collapsible sidebar + icon rail, same
// mobile hamburger drawer. Only the contents differ (docs nav, no auth).
import { ReactNode, useCallback, useEffect, useState } from "react";
import { Link, NavLink, useLocation } from "react-router-dom";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { NAV } from "./content";
import { DOCS_HOME, INVITE, SITE, SOURCE } from "./links";
import { MOBILE, isNarrower, mediaQuery } from "../lib/breakpoints";
import { ICON } from "../icons";
import { savedCollapsePref, initialNavCollapsed } from "../lib/navCollapse";

// Mirrors AppShell's rail behaviour: a persisted desktop collapse choice; small
// viewports default to the icons-only rail / slide-over drawer.
const COLLAPSE_KEY = "dazyflow.docs.sidebar.collapsed";


export function DocsShell({ children }: { children: ReactNode }) {
  const [navCollapsed, setNavCollapsed] = useState<boolean>(() => initialNavCollapsed(COLLAPSE_KEY));
  const location = useLocation();

  // Track the viewport: collapse into the rail below the breakpoint, restore
  // the saved desktop choice above it (matches AppShell).
  useEffect(() => {
    if (typeof window === "undefined" || !window.matchMedia) return;
    const mq = window.matchMedia(mediaQuery(MOBILE));
    const apply = () => setNavCollapsed(mq.matches ? true : savedCollapsePref(COLLAPSE_KEY));
    mq.addEventListener("change", apply);
    return () => mq.removeEventListener("change", apply);
  }, []);

  // Close the mobile drawer after navigating.
  useEffect(() => {
    if (isNarrower(MOBILE)) {
      setNavCollapsed(true);
    }
  }, [location.pathname]);

  const toggleNav = useCallback(() => {
    setNavCollapsed((c) => {
      const next = !c;
      try {
        if (!isNarrower(MOBILE)) {
          localStorage.setItem(COLLAPSE_KEY, next ? "1" : "0");
        }
      } catch {
        /* ignore */
      }
      return next;
    });
  }, []);

  return (
    <div className="app-shell" data-nav-collapsed={navCollapsed ? "true" : "false"}>
      <header className="topbar docs-topbar">
        <button
          className="btn ghost icon hamburger"
          onClick={toggleNav}
          aria-label="Toggle navigation"
          aria-expanded={!navCollapsed}
        >
          <span className="burger" aria-hidden="true">
            <span className="burger-bar burger-top" />
            <span className="burger-bar burger-mid" />
            <span className="burger-bar burger-bot" />
          </span>
        </button>
        {/* A Link, not an <a href="/">. "/" is not in the page map, and nginx
            answers every unmatched path with index.html, so the old brand link
            booted the SPA straight onto "Page not found" — from the one control
            a lost reader is most likely to press. */}
        <Link to={DOCS_HOME} className="brand" title="Dazyflow documentation">
          <img
            src="/logo.svg"
            alt=""
            className="brand-mark-img"
            width={24}
            height={24}
            draggable={false}
          />
          <span className="brand-title">Dazyflow</span>
          {/* The site had no name of its own: "Dazyflow" alone is what the app
              wears, so nothing on screen said which of the two you were in. */}
          <span className="docs-wordmark">Docs</span>
        </Link>
        <div className="spacer" />
        <nav className="docs-topnav" aria-label="Elsewhere">
          <a className="docs-toplink" href={SITE}>
            Product
          </a>
          <a className="docs-toplink" href={SOURCE}>
            Source
          </a>
        </nav>
        {/* Ghost, not primary. A filled accent button is the loudest thing on
            a reference page, and it is pointed away from what the reader came
            for — someone deep in the step catalog is working, not evaluating.
            The invite stays available; it just stops competing with the docs. */}
        <a className="btn ghost sm docs-cta" href={INVITE}>
          Request an invite
        </a>
      </header>

      <div className="body">
        <aside className="sidebar" data-collapsed={navCollapsed ? "true" : "false"}>
          {NAV.map((group) => (
            <div className="docs-nav-group" key={group.text}>
              <div className="group-label">{group.text}</div>
              {group.items.map((item) => {
                const Icon = item.icon;
                return (
                  <NavLink
                    key={item.link}
                    to={item.link}
                    end={item.link.endsWith("/")}
                    title={item.text}
                  >
                    {item.brand ? (
                      // The app's vendor mark (e.g. /brands/gmail.svg).
                      <img
                        className="nav-brand-icon"
                        src={item.brand}
                        alt=""
                        width={18}
                        height={18}
                        draggable={false}
                      />
                    ) : (
                      <Icon size={ICON.lg} />
                    )}
                    <span className="nav-label">{item.text}</span>
                  </NavLink>
                );
              })}
            </div>
          ))}
          <div className="sidebar-spacer" />
          <button
            className="sidebar-collapse-toggle"
            onClick={toggleNav}
            title={navCollapsed ? "Expand sidebar" : "Collapse sidebar"}
          >
            {navCollapsed ? <ChevronRight size={ICON.lg} /> : <ChevronLeft size={ICON.lg} />}
            <span className="nav-label">Collapse</span>
          </button>
        </aside>

        {/* Tapping the scrim closes the mobile drawer (styled in app.css). */}
        <button
          className="sidebar-scrim"
          aria-label="Close navigation"
          onClick={() => setNavCollapsed(true)}
        />

        <main className="main docs-main">{children}</main>
      </div>
    </div>
  );
}
