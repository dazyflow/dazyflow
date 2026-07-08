// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The docs shell REUSES the app's chrome: it renders the exact same markup and
// class names as web/src/components/AppShell.tsx (.app-shell / .topbar /
// .body / .sidebar / .sidebar-scrim / .main + the burger), so app.css styles it
// identically — same top bar, same collapsible sidebar + icon rail, same
// mobile hamburger drawer. Only the contents differ (docs nav, no auth).
import { ReactNode, useCallback, useEffect, useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { NAV } from "./content";

// Mirrors AppShell's rail behaviour: a persisted desktop collapse choice; small
// viewports default to the icons-only rail / slide-over drawer.
const COLLAPSE_KEY = "dazyflow.docs.sidebar.collapsed";
const MOBILE_BREAK = 768;
const INVITE = "mailto:hi@dazyflow.app?subject=Dazyflow%20early%20access";

function savedCollapsePref(): boolean {
  try {
    return localStorage.getItem(COLLAPSE_KEY) === "1";
  } catch {
    return false;
  }
}
function initialNavCollapsed(): boolean {
  if (typeof window !== "undefined" && window.innerWidth <= MOBILE_BREAK) return true;
  return savedCollapsePref();
}

export function DocsShell({ children }: { children: ReactNode }) {
  const [navCollapsed, setNavCollapsed] = useState<boolean>(initialNavCollapsed);
  const location = useLocation();

  // Track the viewport: collapse into the rail below the breakpoint, restore
  // the saved desktop choice above it (matches AppShell).
  useEffect(() => {
    if (typeof window === "undefined" || !window.matchMedia) return;
    const mq = window.matchMedia(`(max-width: ${MOBILE_BREAK}px)`);
    const apply = () => setNavCollapsed(mq.matches ? true : savedCollapsePref());
    mq.addEventListener("change", apply);
    return () => mq.removeEventListener("change", apply);
  }, []);

  // Close the mobile drawer after navigating.
  useEffect(() => {
    if (typeof window !== "undefined" && window.innerWidth <= MOBILE_BREAK) {
      setNavCollapsed(true);
    }
  }, [location.pathname]);

  const toggleNav = useCallback(() => {
    setNavCollapsed((c) => {
      const next = !c;
      try {
        if (window.innerWidth > MOBILE_BREAK) {
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
      <header className="topbar">
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
        <a href="/" className="brand" title="Dazyflow">
          <img
            src="/logo.svg"
            alt=""
            className="brand-mark-img"
            width={24}
            height={24}
            draggable={false}
          />
          <span className="brand-title">Dazyflow</span>
        </a>
        <div className="spacer" />
        <a className="btn primary sm docs-cta" href={INVITE}>
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
                    <Icon size={18} />
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
            {navCollapsed ? <ChevronRight size={18} /> : <ChevronLeft size={18} />}
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
