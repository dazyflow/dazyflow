import { ReactNode, useEffect, useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import {
  Menu,
  LogOut,
  Workflow,
  ShieldCheck,
  Activity,
  Inbox,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  FolderTree,
  Building2,
  Boxes,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { useAuth } from "../auth";

// COLLAPSE_KEY persists the sidebar collapsed/expanded choice across
// reloads. The sidebar is always visible; small viewports just default
// to the icons-only rail until the user expands it.
const COLLAPSE_KEY = "hazyflow.sidebar.collapsed";
// MOBILE_BREAK mirrors the @media (max-width: 768px) rule in app.css —
// AppShell uses it to default new visitors on small viewports to the
// rail layout on first paint.
const MOBILE_BREAK = 768;

export function AppShell({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const {
    token,
    me,
    signOut,
    hasPerm,
    workspaces,
    activeWorkspace,
    setActiveWorkspace,
    tenants,
    activeTenant,
    setActiveTenant,
  } = useAuth();
  // navCollapsed drives the icons-only rail. We persist the user's
  // explicit choice across reloads; if they haven't picked one yet we
  // default to collapsed on small viewports (where the full sidebar
  // would eat too much of a phone/tablet screen) and expanded on
  // desktops. The initial read runs synchronously so the first paint
  // matches — no flicker between the two widths.
  const [navCollapsed, setNavCollapsed] = useState<boolean>(() => {
    try {
      const saved = localStorage.getItem(COLLAPSE_KEY);
      if (saved === "1") return true;
      if (saved === "0") return false;
    } catch {
      /* fall through to viewport default */
    }
    if (typeof window !== "undefined" && window.innerWidth <= MOBILE_BREAK) {
      return true;
    }
    return false;
  });
  useEffect(() => {
    try {
      localStorage.setItem(COLLAPSE_KEY, navCollapsed ? "1" : "0");
    } catch {
      /* localStorage might be blocked in a strict-mode iframe */
    }
  }, [navCollapsed]);
  const location = useLocation();
  // Pending approvals count — surfaces a badge on the sidebar nav so
  // operators see "you have N decisions waiting" without visiting the
  // page. Polled every 30s; updates immediately on visibility change.
  const [pendingCount, setPendingCount] = useState(0);
  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    const fetch = () =>
      api
        // Match the inbox's workspace narrow so the badge count
        // doesn't disagree with what the user sees when they click it.
        .listPendingApprovals(token, {
          workspace: activeWorkspace || undefined,
          tenant: activeTenant || undefined,
        })
        .then((r) => {
          if (!cancelled) setPendingCount(r.approvals?.length ?? 0);
        })
        .catch(() => {
          /* ignore — non-essential */
        });
    void fetch();
    const t = window.setInterval(fetch, 30000);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  }, [token, location.pathname, activeTenant, activeWorkspace]);
  // Editor pages need a full-bleed canvas — remove the main padding.
  // Editor pages need a full-bleed canvas. Match either the canonical
  // /flows/:id or the legacy /pipelines/:id path so an incoming legacy
  // link still gets the right layout during the one-render redirect.
  const inEditor = /^\/(flows|pipelines)\/[^/]+/.test(location.pathname);
  const showAdmin =
    hasPerm("tenant:admin") || hasPerm("graph:admin");

  // The hamburger toggles between the full sidebar and the icons-only
  // rail at every viewport — the sidebar is now always visible, with
  // small screens just defaulting to the rail variant.
  const toggleNav = () => setNavCollapsed((x) => !x);

  return (
    <div className="app-shell" data-nav-collapsed={navCollapsed ? "true" : "false"}>
      <header className="topbar">
        <button
          className="icon ghost hamburger"
          onClick={toggleNav}
          aria-label={t("nav.toggleNav")}
          aria-expanded={!navCollapsed}
        >
          <Menu size={20} />
        </button>
        <div className="brand">
          <img
            src="/favicon.png"
            alt=""
            className="brand-mark-img"
            width={24}
            height={24}
            draggable={false}
          />
          <span className="brand-title">Hazy Flow</span>
        </div>
        <div className="spacer" />
        {me && (
          <div className="user">
            {tenants.length > 1 && (
              <TenantSwitcher
                tenants={tenants}
                activeTenant={activeTenant || me.tenant}
                onPick={setActiveTenant}
              />
            )}
            <span className="topbar-workspace">
              <WorkspaceSwitcher
                tenant={activeTenant || me.tenant}
                activeWorkspace={activeWorkspace || me.workspace}
                workspaces={workspaces}
                onPick={setActiveWorkspace}
                hideTenantPrefix={tenants.length > 1}
              />
            </span>
            <span className="topbar-sep" style={{ color: "var(--faint)" }}>·</span>
            <span className="who topbar-email">{me.subject || t("nav.noSubject")}</span>
            <button className="icon ghost" onClick={signOut} aria-label={t("nav.signOut")}>
              <LogOut size={18} />
            </button>
          </div>
        )}
      </header>
      <div className="body">
        <aside
          className="sidebar"
          data-collapsed={navCollapsed ? "true" : "false"}
        >
          <div className="group-label">{t("nav.workspaceGroup")}</div>
          <NavLink
            to="/flows"
            title={t("nav.flows")}
          >
            <Workflow size={18} />
            <span className="nav-label">{t("nav.flows")}</span>
          </NavLink>
          <NavLink
            to="/runs"
            title={t("nav.runs")}
          >
            <Activity size={18} />
            <span className="nav-label">{t("nav.runs")}</span>
          </NavLink>
          <NavLink
            to="/approvals"
            title={t("nav.approvals")}
          >
            <Inbox size={18} />
            <span className="nav-label" style={{ flex: 1 }}>
              {t("nav.approvals")}
            </span>
            {pendingCount > 0 && (
              <span className="nav-badge">{pendingCount}</span>
            )}
          </NavLink>
          <NavLink
            to="/integrations"
            title={t("nav.integrations")}
          >
            <Boxes size={18} />
            <span className="nav-label">{t("nav.integrations")}</span>
          </NavLink>
          {showAdmin && (
            <>
              <div className="group-label">{t("nav.settingsGroup")}</div>
              <NavLink
                to="/admin"
                title={t("nav.admin")}
              >
                <ShieldCheck size={18} />
                <span className="nav-label">{t("nav.admin")}</span>
              </NavLink>
            </>
          )}
          <div className="sidebar-spacer" />
          {/* Classical collapse arrow pinned to the bottom of the rail.
              Duplicates the hamburger's collapse action so the affordance
              sits next to the panel it controls — the standard pattern
              in VS Code, Linear, etc. Hidden on mobile where the
              hamburger drives a slide-over instead of a rail. */}
          <button
            type="button"
            className="sidebar-collapse-toggle"
            onClick={() => setNavCollapsed((x) => !x)}
            aria-label={
              navCollapsed
                ? t("nav.expandSidebar")
                : t("nav.collapseSidebar")
            }
            title={
              navCollapsed
                ? t("nav.expandSidebar")
                : t("nav.collapseSidebar")
            }
          >
            {navCollapsed ? (
              <ChevronRight size={16} />
            ) : (
              <ChevronLeft size={16} />
            )}
            <span className="nav-label">
              {navCollapsed
                ? t("nav.expandSidebar")
                : t("nav.collapseSidebar")}
            </span>
          </button>
        </aside>
        <main className={"main" + (inEditor ? " no-pad" : "")}>
          {children}
        </main>
      </div>
    </div>
  );
}

// WorkspaceSwitcher renders the tenant/workspace chip. When the
// principal can access more than one workspace, the chip becomes a
// dropdown that lets them pick the active one. Single-workspace
// principals see a flat label.
//
// hideTenantPrefix drops the "tenant/" prefix when a separate tenant
// switcher is already visible — avoids the redundant repetition that
// would otherwise show up for platform admins.
function WorkspaceSwitcher({
  tenant,
  activeWorkspace,
  workspaces,
  onPick,
  hideTenantPrefix,
}: {
  tenant: string;
  activeWorkspace: string;
  workspaces: string[];
  onPick: (ws: string) => void;
  hideTenantPrefix?: boolean;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const multi = workspaces.length > 1;
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (!target.closest(".workspace-switcher")) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);
  const label = hideTenantPrefix ? (
    <strong>{activeWorkspace || t("common.noneParen")}</strong>
  ) : (
    <>
      {tenant}/<strong>{activeWorkspace || t("common.noneParen")}</strong>
    </>
  );
  if (!multi) {
    return (
      <span style={{ fontSize: 13, color: "var(--muted)" }}>{label}</span>
    );
  }
  return (
    <div className="workspace-switcher" style={{ position: "relative" }}>
      <button
        type="button"
        className="ghost"
        onClick={() => setOpen((v) => !v)}
        style={{
          display: "inline-flex",
          alignItems: "center",
          gap: 6,
          fontSize: 13,
          padding: "4px 10px",
        }}
        title={t("nav.switchWorkspace")}
      >
        <FolderTree size={13} />
        <span>{label}</span>
        <ChevronDown size={12} />
      </button>
      {open && (
        <div className="workspace-pop">
          <div className="workspace-pop-head">{tenant}</div>
          {workspaces.map((ws) => (
            <button
              key={ws}
              type="button"
              className={
                "workspace-pop-row" + (ws === activeWorkspace ? " active" : "")
              }
              onClick={() => {
                onPick(ws);
                setOpen(false);
              }}
            >
              {ws}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// TenantSwitcher is the cross-tenant picker shown to platform admins.
// Picking a tenant clears the workspace selection (workspaces are
// tenant-scoped; the post-update whoami pass repopulates).
function TenantSwitcher({
  tenants,
  activeTenant,
  onPick,
}: {
  tenants: string[];
  activeTenant: string;
  onPick: (t: string) => void;
}) {
  const { t: tr } = useTranslation();
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (!target.closest(".tenant-switcher")) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);
  return (
    <div className="tenant-switcher" style={{ position: "relative" }}>
      <button
        type="button"
        className="ghost"
        onClick={() => setOpen((v) => !v)}
        style={{
          display: "inline-flex",
          alignItems: "center",
          gap: 6,
          fontSize: 13,
          padding: "4px 10px",
        }}
        title={tr("nav.switchTenant")}
      >
        <Building2 size={13} />
        <strong>{activeTenant || tr("nav.pickTenant")}</strong>
        <ChevronDown size={12} />
      </button>
      {open && (
        <div className="workspace-pop">
          <div className="workspace-pop-head">{tr("nav.tenants")}</div>
          {tenants.map((t) => (
            <button
              key={t}
              type="button"
              className={
                "workspace-pop-row" + (t === activeTenant ? " active" : "")
              }
              onClick={() => {
                onPick(t);
                setOpen(false);
              }}
            >
              {t}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
