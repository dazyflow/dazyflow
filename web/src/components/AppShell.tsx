import { ReactNode, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { NavLink, useLocation, useNavigate } from "react-router-dom";
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
  LayoutTemplate,
  Plug,
  Settings as SettingsIcon,
  MoreVertical,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { useAuth } from "../auth";
import { ActiveFlowContext } from "../activeFlow";
import { shouldShowTenantID } from "../lib/visibleTenant";
import { orgDisplayName } from "../lib/orgDisplayName";

// COLLAPSE_KEY persists the sidebar collapsed/expanded choice across
// reloads. The sidebar is always visible; small viewports just default
// to the icons-only rail until the user expands it.
const COLLAPSE_KEY = "hazyflow.sidebar.collapsed";
// APPROVAL_SEEN_KEY is the sticky local flag the Approvals nav link
// uses to stay visible after the user has used the approval inbox
// at least once. See the everHadApproval state below.
const APPROVAL_SEEN_KEY = "hazyflow.approvalsEverSeen";
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
  // everHadApproval is a sticky local flag: once a user has seen ANY
  // pending approval in this browser, the Approvals nav link stays
  // visible even after the count drops back to zero — flows with
  // await_approval nodes shouldn't lose their inbox link the moment
  // it empties. New non-tech users (who never touch await_approval)
  // never see the link at all.
  const [everHadApproval, setEverHadApproval] = useState<boolean>(() => {
    try {
      return localStorage.getItem(APPROVAL_SEEN_KEY) === "1";
    } catch {
      return false;
    }
  });
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
          if (cancelled) return;
          const n = r.approvals?.length ?? 0;
          setPendingCount(n);
          if (n > 0 && !everHadApproval) {
            setEverHadApproval(true);
            try {
              localStorage.setItem(APPROVAL_SEEN_KEY, "1");
            } catch {
              /* localStorage might be blocked in a strict-mode iframe */
            }
          }
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
  }, [token, location.pathname, activeTenant, activeWorkspace, everHadApproval]);
  // Editor pages need a full-bleed canvas — remove the main padding.
  // Editor pages need a full-bleed canvas. Match either the canonical
  // /flows/:id or the legacy /pipelines/:id path so an incoming legacy
  // link still gets the right layout during the one-render redirect.
  const inEditor = /^\/(flows|pipelines)\/[^/]+/.test(location.pathname);
  const showAdmin =
    hasPerm("tenant:admin") || hasPerm("graph:admin");

  // activeFlowName is published by the editor (via ActiveFlowContext) so
  // the top bar can show which flow is open in place of the wordmark.
  // Cleared whenever we navigate away from an editor route so a stale
  // name never lingers on the flow list / runs / admin pages.
  const [activeFlowName, setActiveFlowName] = useState<string | null>(null);
  // openSettings is registered by the editor so the top-right "current
  // flow" three-dots can open the flow-settings modal it owns. Stored
  // as a 0-arg callback; null when no editor is mounted.
  const [openSettings, setOpenSettings] = useState<(() => void) | null>(null);
  useEffect(() => {
    if (!inEditor) {
      setActiveFlowName(null);
      setOpenSettings(null);
    }
  }, [inEditor]);

  // The hamburger toggles between the full sidebar and the icons-only
  // rail at every viewport — the sidebar is now always visible, with
  // small screens just defaulting to the rail variant.
  const toggleNav = () => setNavCollapsed((x) => !x);

  return (
    <ActiveFlowContext.Provider
      value={{
        name: activeFlowName,
        setName: setActiveFlowName,
        openSettings,
        setOpenSettings,
      }}
    >
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
        {/* The logo is the home affordance — clicking it lands on the
            start/welcome screen (where no flow is selected), matching
            the sibling `hazy` app's brand-click-goes-home behaviour. */}
        <NavLink to="/welcome" className="brand" title="Hazy Flow">
          <img
            src="/favicon.png"
            alt=""
            className="brand-mark-img"
            width={24}
            height={24}
            draggable={false}
          />
          {/* In the editor, the open flow's name takes the wordmark's
              slot so the operator always knows which flow they're in.
              Elsewhere it's the product wordmark. */}
          <span className="brand-title">
            {inEditor && activeFlowName ? activeFlowName : "Hazy Flow"}
          </span>
        </NavLink>
        <div className="spacer" />
        {me && (
          <div className="user">
            {tenants.length > 1 && (
              <TenantSwitcher
                tenants={tenants}
                activeTenant={activeTenant || me.tenant}
                onPick={setActiveTenant}
                nameOf={(tid) => orgDisplayName(me, tid)}
              />
            )}
            <span className="topbar-workspace">
              <WorkspaceSwitcher
                tenant={activeTenant || me.tenant}
                activeWorkspace={activeWorkspace || me.workspace}
                workspaces={workspaces}
                onPick={setActiveWorkspace}
                // The tenant prefix is only meaningful chrome for
                // principals who actually navigate between tenants —
                // platform admins or anyone with >1 tenant on their
                // token. For ordinary single-tenant users, the
                // identifier `usr_…` is internal jargon that adds
                // noise without surfacing an actionable choice.
                hideTenantPrefix={!shouldShowTenantID(me, tenants.length)}
              />
            </span>
            {inEditor && openSettings && (
              <FlowMenu onOpenSettings={openSettings} />
            )}
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
            to="/templates"
            title={t("nav.templates")}
          >
            <LayoutTemplate size={18} />
            <span className="nav-label">{t("nav.templates")}</span>
          </NavLink>
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
          {/* Approvals lives in the sidebar only when it's actually
              useful: any pending approval right now, a sticky flag
              from a previous visit, or an admin who needs to know
              one might exist. Non-tech buyers whose flows don't use
              await_approval never see the link, removing a confusing
              "what's this?" entry from their default sidebar. */}
          {(pendingCount > 0 || everHadApproval || hasPerm("tenant:admin")) && (
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
          )}
          <NavLink
            to="/connections"
            title={t("nav.connections")}
          >
            <Plug size={18} />
            <span className="nav-label">{t("nav.connections")}</span>
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
          {/* Classical collapse arrow. Duplicates the hamburger's
              collapse action so the affordance sits next to the panel it
              controls — the standard pattern in VS Code, Linear, etc.
              Hidden on mobile where the hamburger drives a slide-over. */}
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
          {/* Account control pinned to the very bottom of the sidebar —
              the entry point to per-user actions (Settings, Sign out).
              Mirrors the sibling `hazy` app's sidebar-footer account
              menu. Shows just the user icon in the collapsed rail. */}
          {me && (
            <AccountMenu
              email={me.subject || t("nav.noSubject")}
              onSignOut={signOut}
              collapsed={navCollapsed}
            />
          )}
        </aside>
        <main className={"main" + (inEditor ? " no-pad" : "")}>
          {children}
        </main>
      </div>
    </div>
    </ActiveFlowContext.Provider>
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
  nameOf,
}: {
  tenants: string[];
  activeTenant: string;
  onPick: (t: string) => void;
  // nameOf resolves a tenant ID to its display name (falls back to the
  // ID itself when no profile is set). Keeps the switcher decoupled
  // from the WhoAmI shape so the same control could be reused
  // elsewhere with a different source.
  nameOf: (tenant: string) => string;
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
        <strong>
          {activeTenant ? nameOf(activeTenant) : tr("nav.pickTenant")}
        </strong>
        <ChevronDown size={12} />
      </button>
      {open && (
        <div className="workspace-pop">
          <div className="workspace-pop-head">{tr("nav.tenants")}</div>
          {tenants.map((tid) => {
            const label = nameOf(tid);
            return (
              <button
                key={tid}
                type="button"
                className={
                  "workspace-pop-row" + (tid === activeTenant ? " active" : "")
                }
                onClick={() => {
                  onPick(tid);
                  setOpen(false);
                }}
                title={tid}
              >
                <span>{label}</span>
                {label !== tid && (
                  <span className="workspace-pop-id">{tid}</span>
                )}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

// AccountMenu is the lower-left sidebar account control — the entry
// point to per-user actions (Settings, Sign out). Modelled on the
// sibling `hazy` app, whose account/settings menu lives in the sidebar
// footer. The trigger shows a user icon + email (the email hides via
// .nav-label in the collapsed rail, leaving just the icon); the menu
// pops upward so it doesn't run off the bottom of the viewport.
function AccountMenu({
  email,
  onSignOut,
  collapsed,
}: {
  email: string;
  onSignOut: () => void;
  collapsed: boolean;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  // The pop is rendered in a portal at document.body (see below) so the
  // sidebar's overflow clip + stacking context can't hide it behind the
  // editor canvas. That means we position it ourselves from the
  // trigger's on-screen rect: anchored above the trigger (bottom-up).
  const [pos, setPos] = useState<{ left: number; bottom: number } | null>(null);
  const place = () => {
    const r = triggerRef.current?.getBoundingClientRect();
    if (!r) return;
    setPos({ left: r.left, bottom: window.innerHeight - r.top + 6 });
  };
  useEffect(() => {
    if (!open) return;
    place();
    const onDown = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (!target.closest(".account-menu") && !target.closest(".account-pop")) {
        setOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    // Re-place on resize/scroll so the fixed pop tracks the trigger.
    const onReflow = () => place();
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    window.addEventListener("resize", onReflow);
    window.addEventListener("scroll", onReflow, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("resize", onReflow);
      window.removeEventListener("scroll", onReflow, true);
    };
  }, [open]);
  return (
    <div className="account-menu">
      <button
        ref={triggerRef}
        type="button"
        className="account-menu-trigger"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("account.menu")}
        title={collapsed ? email : undefined}
      >
        <SettingsIcon size={18} />
        <span className="nav-label account-email">{email}</span>
        <ChevronDown size={14} className="nav-label account-chevron" />
      </button>
      {open && pos &&
        createPortal(
          <div
            className="workspace-pop account-pop"
            role="menu"
            style={{
              position: "fixed",
              left: pos.left,
              bottom: pos.bottom,
              top: "auto",
              right: "auto",
            }}
          >
            <button
              type="button"
              role="menuitem"
              className="workspace-pop-row account-pop-row"
              onClick={() => {
                setOpen(false);
                navigate("/settings");
              }}
            >
              <SettingsIcon size={14} />
              {t("account.settings")}
            </button>
            <div className="workspace-pop-sep" role="separator" />
            <button
              type="button"
              role="menuitem"
              className="workspace-pop-row account-pop-row danger"
              onClick={() => {
                setOpen(false);
                onSignOut();
              }}
            >
              <LogOut size={14} />
              {t("account.signOut")}
            </button>
          </div>,
          document.body,
        )}
    </div>
  );
}

// FlowMenu is the top-right three-dots menu acting on the current flow
// (mirrors hazy's per-context menu). Today it has one action — open the
// flow-settings modal, which lives inside the editor and is invoked via
// the callback the editor registered on ActiveFlowContext.
function FlowMenu({ onOpenSettings }: { onOpenSettings: () => void }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (!target.closest(".flow-menu")) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return (
    <div className="flow-menu" style={{ position: "relative" }}>
      <button
        type="button"
        className="icon ghost"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("flowMenu.label")}
        title={t("flowMenu.label")}
      >
        <MoreVertical size={18} />
      </button>
      {open && (
        <div className="workspace-pop account-pop" role="menu">
          <button
            type="button"
            role="menuitem"
            className="workspace-pop-row account-pop-row"
            onClick={() => {
              setOpen(false);
              onOpenSettings();
            }}
          >
            <SettingsIcon size={14} />
            {t("flowMenu.settings")}
          </button>
        </div>
      )}
    </div>
  );
}
