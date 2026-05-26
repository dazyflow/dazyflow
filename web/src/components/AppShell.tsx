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
  FolderTree,
  Building2,
  Boxes,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { useAuth } from "../auth";

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
  const [navOpen, setNavOpen] = useState(false);
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

  return (
    <div className="app-shell">
      <header className="topbar">
        <button
          className="icon ghost hamburger"
          onClick={() => setNavOpen((x) => !x)}
          aria-label={t("nav.toggleNav")}
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
            {tenants.length > 1 && (
              <TenantSwitcher
                tenants={tenants}
                activeTenant={activeTenant || me.tenant}
                onPick={setActiveTenant}
              />
            )}
            <WorkspaceSwitcher
              tenant={activeTenant || me.tenant}
              activeWorkspace={activeWorkspace || me.workspace}
              workspaces={workspaces}
              onPick={setActiveWorkspace}
              hideTenantPrefix={tenants.length > 1}
            />
            <span style={{ color: "var(--faint)" }}>·</span>
            <span className="who">{me.subject || t("nav.noSubject")}</span>
            <button className="icon ghost" onClick={signOut} aria-label={t("nav.signOut")}>
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
          <div className="group-label">{t("nav.workspaceGroup")}</div>
          <NavLink to="/flows" onClick={() => setNavOpen(false)}>
            <Workflow size={18} />
            {t("nav.flows")}
          </NavLink>
          <NavLink to="/runs" onClick={() => setNavOpen(false)}>
            <Activity size={18} />
            {t("nav.runs")}
          </NavLink>
          <NavLink to="/approvals" onClick={() => setNavOpen(false)}>
            <Inbox size={18} />
            <span style={{ flex: 1 }}>{t("nav.approvals")}</span>
            {pendingCount > 0 && (
              <span className="nav-badge">{pendingCount}</span>
            )}
          </NavLink>
          <NavLink to="/integrations" onClick={() => setNavOpen(false)}>
            <Boxes size={18} />
            {t("nav.integrations")}
          </NavLink>
          {showAdmin && (
            <>
              <div className="group-label">{t("nav.settingsGroup")}</div>
              <NavLink to="/admin" onClick={() => setNavOpen(false)}>
                <ShieldCheck size={18} />
                {t("nav.admin")}
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
