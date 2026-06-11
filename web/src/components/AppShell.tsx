import { ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { NavLink, useLocation, useNavigate } from "react-router-dom";
import {
  Menu,
  MailWarning,
  LogOut,
  Workflow,
  ShieldCheck,
  Activity,
  Gauge,
  Inbox,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  FolderTree,
  Building2,
  Boxes,
  LayoutTemplate,
  Key,
  Settings as SettingsIcon,
  MoreVertical,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { useAuth } from "../auth";
import { ActiveFlowContext, FLOWS_CHANGED_EVENT } from "../activeFlow";
import { shouldShowTenantID } from "../lib/visibleTenant";
import { orgDisplayName } from "../lib/orgDisplayName";
import { FlowIcon } from "../icons";
import { isImageIcon } from "../lib/iconImage";
import type { FlowSummary } from "../types";

// orgGlyph renders an org's icon: an uploaded image (data: URL) as an
// <img>, otherwise the generic Building2 mark. Shared by the tenant
// switcher trigger + rows.
function orgGlyph(icon: string | undefined, size: number) {
  return isImageIcon(icon) ? (
    <img
      src={icon}
      alt=""
      width={size}
      height={size}
      style={{ borderRadius: 3, objectFit: "contain" }}
      draggable={false}
    />
  ) : (
    <Building2 size={size} />
  );
}

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

// savedCollapsePref reads the user's explicit desktop collapse choice.
// Only the desktop toggle writes this (see toggleNav) — on small screens
// the rail is a per-session default, not a persisted preference, so a
// phone toggle never overwrites the desktop layout.
function savedCollapsePref(): boolean {
  try {
    return localStorage.getItem(COLLAPSE_KEY) === "1";
  } catch {
    return false;
  }
}

// initialNavCollapsed picks the first-paint rail state: small viewports
// always start collapsed (the full sidebar would eat too much of a
// phone/tablet screen, regardless of any saved desktop preference);
// desktops honour the saved choice. Runs synchronously so the first
// paint matches the viewport — no flicker between the two widths.
function initialNavCollapsed(): boolean {
  if (typeof window !== "undefined" && window.innerWidth <= MOBILE_BREAK) {
    return true;
  }
  return savedCollapsePref();
}

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
  // Daemon version for the account-menu footer. Public GET /api/v1, so
  // no token needed; fetched once on mount. Stays null (footer hidden)
  // if the request fails — it's purely informational.
  const [serverVersion, setServerVersion] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    api
      .serviceInfo()
      .then((info) => {
        if (live) setServerVersion(info.build?.version || null);
      })
      .catch(() => {
        /* informational only — leave the footer hidden on failure */
      });
    return () => {
      live = false;
    };
  }, []);
  // navCollapsed drives the icons-only rail. We persist the user's
  // explicit choice across reloads; if they haven't picked one yet we
  // default to collapsed on small viewports (where the full sidebar
  // would eat too much of a phone/tablet screen) and expanded on
  // desktops. The initial read runs synchronously so the first paint
  // matches — no flicker between the two widths.
  const [navCollapsed, setNavCollapsed] = useState<boolean>(initialNavCollapsed);
  // Keep the rail in sync with the viewport: collapse when we cross into
  // a small screen, and restore the saved desktop preference when we
  // cross back out. matchMedia's change event fires only on crossing the
  // breakpoint (not on every resize), so a user who expands the rail on a
  // phone keeps it expanded for the session. Viewport-driven changes are
  // deliberately NOT persisted — the stored value is the desktop choice.
  useEffect(() => {
    if (typeof window === "undefined" || !window.matchMedia) return;
    const mq = window.matchMedia(`(max-width: ${MOBILE_BREAK}px)`);
    const apply = () => setNavCollapsed(mq.matches ? true : savedCollapsePref());
    mq.addEventListener("change", apply);
    return () => mq.removeEventListener("change", apply);
  }, []);
  const location = useLocation();
  // On small screens, close the slide-in drawer after navigating — the
  // user picked a menu item, so the drawer has done its job and
  // shouldn't keep covering the page. No-op on desktop (inline sidebar)
  // and on first mount (the drawer already starts closed there).
  useEffect(() => {
    if (typeof window !== "undefined" && window.innerWidth <= MOBILE_BREAK) {
      setNavCollapsed(true);
    }
  }, [location.pathname]);
  // Pending approvals count — surfaces a badge on the sidebar nav so
  // operators see "you have N decisions waiting" without visiting the
  // page. Polled every 30s; updates immediately on visibility change.
  const [pendingCount, setPendingCount] = useState(0);
  // All flows in the active workspace, listed under the "Flows" nav entry.
  const [flows, setFlows] = useState<FlowSummary[]>([]);
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
  // Load the workspace's flows for the sidebar list.
  const refreshFlows = useCallback(() => {
    if (!token || !activeWorkspace) {
      setFlows([]);
      return;
    }
    api
      .listGraphs(token, activeTenant, activeWorkspace)
      .then((r) => setFlows(r.graphs))
      .catch(() => {
        /* ignore — sidebar list is non-essential */
      });
  }, [token, activeTenant, activeWorkspace]);
  // Refetch on navigation (covers create/delete via the flow list).
  useEffect(refreshFlows, [refreshFlows, location.pathname]);
  // Refetch immediately when a flow's name/icon is saved in the editor —
  // the editor fires FLOWS_CHANGED_EVENT after the save persists, so the
  // sidebar reflects a renamed flow / new icon without a navigation.
  useEffect(() => {
    const onChanged = () => refreshFlows();
    window.addEventListener(FLOWS_CHANGED_EVENT, onChanged);
    return () => window.removeEventListener(FLOWS_CHANGED_EVENT, onChanged);
  }, [refreshFlows]);
  // Editor pages need a full-bleed canvas — remove the main padding.
  // Editor pages need a full-bleed canvas. Match either the canonical
  // /flows/:id or the legacy /pipelines/:id path so an incoming legacy
  // link still gets the right layout during the one-render redirect.
  const inEditor = /^\/(flows|pipelines)\/[^/]+/.test(location.pathname);
  const showAdmin =
    hasPerm("organization:admin") || hasPerm("graph:admin");

  // activeFlowName is published by the editor (via ActiveFlowContext) so
  // the top bar can show which flow is open in place of the wordmark.
  // Cleared whenever we navigate away from an editor route so a stale
  // name never lingers on the flow list / runs / admin pages.
  const [activeFlowName, setActiveFlowName] = useState<string | null>(null);
  // activeFlowIcon mirrors the open flow's icon so the top bar can show
  // it next to the name (only when the flow has a non-default icon set).
  const [activeFlowIcon, setActiveFlowIcon] = useState<string | null>(null);
  // openSettings is registered by the editor so the top-right "current
  // flow" three-dots can open the flow-settings modal it owns. Stored
  // as a 0-arg callback; null when no editor is mounted.
  const [openSettings, setOpenSettings] = useState<(() => void) | null>(null);
  useEffect(() => {
    if (!inEditor) {
      setActiveFlowName(null);
      setActiveFlowIcon(null);
      setOpenSettings(null);
    }
  }, [inEditor]);

  // The hamburger toggles between the full sidebar and the icons-only
  // rail (full ↔ icons-only) on desktop, and the slide-in drawer
  // (open ↔ off-canvas) on small screens. We persist the choice only on
  // desktop: on a small screen the open/closed state is transient, so a
  // phone toggle must not overwrite the saved desktop layout (the
  // matchMedia listener re-applies the right state on breakpoint cross).
  const toggleNav = () =>
    setNavCollapsed((x) => {
      const next = !x;
      if (typeof window === "undefined" || window.innerWidth > MOBILE_BREAK) {
        try {
          localStorage.setItem(COLLAPSE_KEY, next ? "1" : "0");
        } catch {
          /* localStorage might be blocked in a strict-mode iframe */
        }
      }
      return next;
    });
  // closeNav always closes (collapses) — used by the mobile drawer's
  // scrim and on navigation. No persistence: closing the drawer is a
  // mobile-transient action, not a desktop layout preference.
  const closeNav = () => setNavCollapsed(true);

  // Top-bar branding. In the editor the open flow takes the slot; the
  // org's name/logo replaces the product wordmark once the org has a
  // profile set; otherwise it's the Hazyflow mark.
  const curTenant = activeTenant || me?.tenant || "";
  const orgMembership = me?.memberships?.find((m) => m.tenant === curTenant);
  const orgName = orgMembership?.display_name;
  const orgIcon = orgMembership?.icon;
  const showFlow = inEditor && !!activeFlowName;

  return (
    <ActiveFlowContext.Provider
      value={{
        name: activeFlowName,
        setName: setActiveFlowName,
        icon: activeFlowIcon,
        setIcon: setActiveFlowIcon,
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
        <NavLink to="/welcome" className="brand" title={orgName || "Hazyflow"}>
          {/* Mark: the open flow's icon in the editor, else the org logo
              when set, else the Hazyflow favicon. */}
          {showFlow && activeFlowIcon ? (
            <FlowIcon icon={activeFlowIcon} size={22} className="brand-flow-icon" />
          ) : !showFlow && isImageIcon(orgIcon) ? (
            <img
              src={orgIcon}
              alt=""
              className="flow-icon-img brand-flow-icon"
              width={24}
              height={24}
              draggable={false}
            />
          ) : (
            <img
              src="/favicon.png"
              alt=""
              className="brand-mark-img"
              width={24}
              height={24}
              draggable={false}
            />
          )}
          {/* Title: the open flow's name in the editor; otherwise the
              org's name once set, falling back to the product wordmark. */}
          <span className="brand-title">
            {showFlow ? activeFlowName : orgName || "Hazyflow"}
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
                iconOf={(tid) =>
                  me.memberships?.find((m) => m.tenant === tid)?.icon
                }
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
          {/* All flows in the workspace, nested under the Flows entry.
              Hidden in the collapsed icon-rail (no room for labels). */}
          {!navCollapsed && flows.length > 0 && (
            <div className="nav-flows">
              {flows.map((f) => (
                <NavLink
                  key={f.id}
                  to={`/flows/${encodeURIComponent(f.id)}`}
                  className="nav-flow-item"
                  title={f.name || f.id}
                >
                  <FlowIcon icon={f.icon} size={15} className="nav-flow-icon" />
                  <span className="nav-label nav-flow-name">{f.name || f.id}</span>
                </NavLink>
              ))}
            </div>
          )}
          <NavLink
            to="/runs"
            title={t("nav.runs")}
          >
            <Activity size={18} />
            <span className="nav-label">{t("nav.runs")}</span>
          </NavLink>
          <NavLink
            to="/usage"
            title={t("nav.usage")}
          >
            <Gauge size={18} />
            <span className="nav-label">{t("nav.usage")}</span>
          </NavLink>
          {/* Approvals lives in the sidebar only when it's actually
              useful: any pending approval right now, a sticky flag
              from a previous visit, or an admin who needs to know
              one might exist. Non-tech buyers whose flows don't use
              await_approval never see the link, removing a confusing
              "what's this?" entry from their default sidebar. */}
          {(pendingCount > 0 || everHadApproval || hasPerm("organization:admin")) && (
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
          <div className="group-label">{t("nav.appsGroup")}</div>
          {/* "Secrets" only appears for users who can actually manage
              them (secret:write — the editor role has it, viewers don't).
              Without it the page is just hidden sections, so we keep it
              out of the menu rather than show a non-techie a dead
              surface. */}
          {hasPerm("secret:write") && (
            <NavLink
              to="/secrets"
              title={t("nav.connections")}
            >
              <Key size={18} />
              <span className="nav-label">{t("nav.connections")}</span>
            </NavLink>
          )}
          <NavLink
            to="/apps"
            title={t("nav.integrations")}
          >
            <Boxes size={18} />
            <span className="nav-label">{t("nav.integrations")}</span>
          </NavLink>
          <div className="sidebar-spacer" />
          {/* Classical collapse arrow. Duplicates the hamburger's
              collapse action so the affordance sits next to the panel it
              controls — the standard pattern in VS Code, Linear, etc.
              Hidden on mobile where the hamburger drives a slide-over. */}
          <button
            type="button"
            className="sidebar-collapse-toggle"
            onClick={toggleNav}
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
              showAdmin={showAdmin}
              version={serverVersion}
            />
          )}
        </aside>
        {/* Scrim behind the mobile drawer — tap to close. Inert on
            desktop (CSS hides it); only interactive on small screens
            while the drawer is open. */}
        <button
          type="button"
          className="sidebar-scrim"
          aria-label={t("nav.closeMenu")}
          tabIndex={navCollapsed ? -1 : 0}
          onClick={closeNav}
        />
        <main className={"main" + (inEditor ? " no-pad" : "")}>
          {/* "Confirm your email" nag — only on verification-active
              deployments for unverified password accounts. Hidden in the
              editor so it never eats canvas height. */}
          {me?.verification_pending && !inEditor && <VerifyEmailBanner />}
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
      <span style={{ fontSize: "var(--text-md)", color: "var(--muted)" }}>{label}</span>
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
          fontSize: "var(--text-md)",
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
  iconOf,
}: {
  tenants: string[];
  activeTenant: string;
  onPick: (t: string) => void;
  // nameOf resolves a tenant ID to its display name (falls back to the
  // ID itself when no profile is set). Keeps the switcher decoupled
  // from the WhoAmI shape so the same control could be reused
  // elsewhere with a different source.
  nameOf: (tenant: string) => string;
  // iconOf resolves a tenant ID to its org icon (data: URL / name), if any.
  iconOf: (tenant: string) => string | undefined;
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
          fontSize: "var(--text-md)",
          padding: "4px 10px",
        }}
        title={tr("nav.switchTenant")}
      >
        {orgGlyph(iconOf(activeTenant), 14)}
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
                <span className="tenant-pop-glyph">{orgGlyph(iconOf(tid), 14)}</span>
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
  showAdmin,
  version,
}: {
  email: string;
  onSignOut: () => void;
  collapsed: boolean;
  showAdmin: boolean;
  version: string | null;
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
            {showAdmin && (
              <button
                type="button"
                role="menuitem"
                className="workspace-pop-row account-pop-row"
                onClick={() => {
                  setOpen(false);
                  navigate("/admin");
                }}
              >
                <ShieldCheck size={14} />
                {t("nav.admin")}
              </button>
            )}
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
            {/* Version footer — informational, not a menu item. Hidden
                until the GET /api/v1 fetch resolves. "dev" on an
                unstamped local build; a release shows e.g. "v0.1.0". */}
            {version && (
              <>
                <div className="workspace-pop-sep" role="separator" />
                <div className="account-pop-version">hzd v{version}</div>
              </>
            )}
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

// VerifyEmailBanner nags an unverified account to confirm its address,
// with a resend button. Dismissal is per-render-tree only (state, not
// storage) — the nag should come back next visit until verified.
function VerifyEmailBanner() {
  const { t } = useTranslation();
  const { token, refreshMe } = useAuth();
  const [sent, setSent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [hidden, setHidden] = useState(false);

  if (hidden) return null;
  const resend = async () => {
    if (!token) return;
    setBusy(true);
    try {
      const r = await api.resendVerification(token);
      if (r.already_verified) {
        await refreshMe(); // banner disappears via whoami
        return;
      }
      setSent(true);
    } catch {
      // The button stays — the user can retry.
    } finally {
      setBusy(false);
    }
  };
  return (
    <div
      className="card"
      style={{
        display: "flex",
        alignItems: "center",
        gap: "var(--space-3)",
        marginBottom: "var(--space-4)",
        borderColor: "var(--warning, #d97706)",
      }}
    >
      <MailWarning size={18} style={{ color: "var(--warning, #d97706)", flexShrink: 0 }} />
      <div style={{ flex: 1 }}>
        {sent ? t("verifyEmail.bannerSent") : t("verifyEmail.banner")}
      </div>
      {!sent && (
        <button className="ghost" disabled={busy} onClick={() => void resend()}>
          {busy ? t("verifyEmail.bannerSending") : t("verifyEmail.bannerResend")}
        </button>
      )}
      <button
        type="button"
        className="icon-button"
        onClick={() => setHidden(true)}
        aria-label={t("verifyEmail.bannerDismiss")}
        title={t("verifyEmail.bannerDismiss")}
      >
        <X size={14} />
      </button>
    </div>
  );
}
