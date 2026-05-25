import { ReactNode, useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { Menu, LogOut, Workflow, ShieldCheck } from "lucide-react";
import { useAuth } from "../auth";

export function AppShell({ children }: { children: ReactNode }) {
  const { me, signOut, hasPerm } = useAuth();
  const [navOpen, setNavOpen] = useState(false);
  const location = useLocation();
  // Editor pages need a full-bleed canvas — remove the main padding.
  const inEditor = /^\/pipelines\/[^/]+/.test(location.pathname);
  const showAdmin =
    hasPerm("tenant:admin") || hasPerm("graph:admin");

  return (
    <div className="app-shell">
      <header className="topbar">
        <button
          className="icon ghost hamburger"
          onClick={() => setNavOpen((x) => !x)}
          aria-label="toggle navigation"
        >
          <Menu size={20} />
        </button>
        <div className="brand">
          <span className="brand-mark">∼</span>
          <span>Hazy Flow</span>
        </div>
        <div className="spacer" />
        {me && (
          <div className="user">
            <span className="who">{me.subject || "(no subject)"}</span>
            <span>·</span>
            <span>
              {me.tenant}/{me.workspace}
            </span>
            <button className="icon ghost" onClick={signOut} aria-label="sign out">
              <LogOut size={18} />
            </button>
          </div>
        )}
      </header>
      <div className="body">
        {navOpen && (
          <div
            className="sidebar-backdrop"
            onClick={() => setNavOpen(false)}
          />
        )}
        <aside className="sidebar" data-open={navOpen ? "true" : "false"}>
          <div className="group-label">Workspace</div>
          <NavLink to="/pipelines" onClick={() => setNavOpen(false)}>
            <Workflow size={18} />
            Pipelines
          </NavLink>
          {showAdmin && (
            <>
              <div className="group-label">Settings</div>
              <NavLink to="/admin" onClick={() => setNavOpen(false)}>
                <ShieldCheck size={18} />
                Admin
              </NavLink>
            </>
          )}
        </aside>
        <main className={"main" + (inEditor ? " no-pad" : "")}>
          {children}
        </main>
      </div>
    </div>
  );
}
