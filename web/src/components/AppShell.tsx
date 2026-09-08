// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { NavLink, useLocation, useNavigate } from "react-router-dom";
import {
  MailWarning,
  LogOut,
  Workflow,
  ShieldCheck,
  Activity,
  LifeBuoy,
  PencilLine,
  Table2,
  Gauge,
  Inbox,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  FolderTree,
  Building2,
  Boxes,
  Plug,
  Plus,
  Send,
  Settings as SettingsIcon,
  MoreVertical,
  HelpCircle,
  CreditCard,
  Sparkles,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { useAuth } from "../auth";
import { ActiveFlowContext, FLOWS_CHANGED_EVENT } from "../activeFlow";
import { shouldShowTenantID } from "../lib/visibleTenant";
import { tenantDisplayName } from "../lib/orgDisplayName";
import { downloadJson } from "../lib/download";
import { Button } from "./ui/Button";
import { useAnchoredPop } from "./ui/useAnchoredPop";
import { OrgSwitcherModal } from "./dialogs/OrgSwitcherModal";
import { ConnectMcpClientModal } from "./dialogs/ConnectMcpClientModal";
import { CommandPalette } from "./CommandPalette";
import { HelpModal } from "./dialogs/HelpModal";
import { FlowIcon, ICON } from "../icons";
import { isImageIcon } from "../lib/iconImage";
import { MOBILE, isNarrower, mediaQuery } from "../lib/breakpoints";
import type { FlowSummary } from "../types";
import { POLL } from "../lib/timing";
import { savedCollapsePref, initialNavCollapsed } from "../lib/navCollapse";
import { resendOutcome } from "../lib/verifyResend";

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

const COLLAPSE_KEY = "dazyflow.sidebar.collapsed";
const APPROVAL_SEEN_KEY = "dazyflow.approvalsEverSeen";

export function AppShell({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const {
    token,
    me,
    signOut,
    hasPerm,
    activeWorkspace,
    tenants,
    activeTenant,
    setActiveTenant,
    reloadTenants,
  } = useAuth();
  const [orgModalOpen, setOrgModalOpen] = useState(false);
  const [connectingMcp, setConnectingMcp] = useState(false);
  // A public endpoint, so it needs no auth and cannot fail the shell.
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
  // Persisted per-viewer; localStorage access can throw, so it is guarded.
  const [navCollapsed, setNavCollapsed] = useState<boolean>(() =>
    initialNavCollapsed(COLLAPSE_KEY),
  );
  // Collapses on crossing into the narrow breakpoint, not on every resize.
  useEffect(() => {
    if (typeof window === "undefined" || !window.matchMedia) return;
    const mq = window.matchMedia(mediaQuery(MOBILE));
    const apply = () =>
      setNavCollapsed(mq.matches ? true : savedCollapsePref(COLLAPSE_KEY));
    mq.addEventListener("change", apply);
    return () => mq.removeEventListener("change", apply);
  }, []);
  const location = useLocation();
  useEffect(() => {
    if (isNarrower(MOBILE)) {
      setNavCollapsed(true);
    }
  }, [location.pathname]);
  const [pendingCount, setPendingCount] = useState(0);
  const [supportUnread, setSupportUnread] = useState(0);
  const [flows, setFlows] = useState<FlowSummary[]>([]);
  const [cmdOpen, setCmdOpen] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  // Sticky: the nav entry must not vanish once a user has seen an approval.
  const [everHadApproval, setEverHadApproval] = useState<boolean>(() => {
    try {
      return localStorage.getItem(APPROVAL_SEEN_KEY) === "1";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    if (!token || !me) return;
    let cancelled = false;
    const fetch = () =>
      api
        .countPendingApprovals(token, {
          workspace: activeWorkspace || undefined,
          tenant: activeTenant || undefined,
        })
        .then((r) => {
          if (cancelled) return;
          const n = r.count ?? 0;
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
    const t = window.setInterval(fetch, POLL.background);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  }, [
    token,
    me,
    location.pathname,
    activeTenant,
    activeWorkspace,
    everHadApproval,
  ]);
  const isAgent = hasPerm("support:agent");
  const supportOn = !!me?.support_tickets_enabled || isAgent;
  useEffect(() => {
    if (!token || !supportOn) {
      setSupportUnread(0);
      return;
    }
    let cancelled = false;
    const fetch = () => {
      const p = isAgent
        ? api
            .ticketQueueSummary(token)
            .then((r) => r.summary?.by_status?.awaiting_support ?? 0)
        : api
            .listMyTickets(token, "awaiting_user")
            .then((r) => r.tickets?.length ?? 0);
      p.then((n) => {
        if (!cancelled) setSupportUnread(n);
      }).catch(() => {
        /* ignore — non-essential */
      });
    };
    fetch();
    const iv = window.setInterval(fetch, POLL.background);
    return () => {
      cancelled = true;
      window.clearInterval(iv);
    };
  }, [token, supportOn, isAgent, location.pathname]);
  const refreshFlows = useCallback(() => {
    if (!token || !me || !activeWorkspace) {
      setFlows([]);
      return;
    }
    api
      .listGraphs(token, activeTenant, activeWorkspace)
      .then((r) => setFlows(r.graphs))
      .catch(() => {
        /* ignore — sidebar list is non-essential */
      });
  }, [token, me, activeTenant, activeWorkspace]);
  useEffect(refreshFlows, [refreshFlows, location.pathname]);
  useEffect(() => {
    const onChanged = () => refreshFlows();
    window.addEventListener(FLOWS_CHANGED_EVENT, onChanged);
    return () => window.removeEventListener(FLOWS_CHANGED_EVENT, onChanged);
  }, [refreshFlows]);
  const inEditor =
    /^\/(flows|pipelines)\/(?!new(?:$|\/))[^/]+/.test(location.pathname) ||
    /^\/support\/flows\//.test(location.pathname);
  const showAdmin = hasPerm("organization:admin") || hasPerm("graph:admin");

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K")) {
        if (inEditor) return; // editor's step palette handles it
        e.preventDefault();
        setCmdOpen((v) => !v);
        return;
      }
      if (e.key === "?" && !e.metaKey && !e.ctrlKey && !e.altKey) {
        const el = e.target as HTMLElement | null;
        const typing =
          !!el &&
          (el.tagName === "INPUT" ||
            el.tagName === "TEXTAREA" ||
            el.isContentEditable);
        if (typing) return;
        e.preventDefault();
        setHelpOpen((v) => !v);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [inEditor]);

  const [activeFlowName, setActiveFlowName] = useState<string | null>(null);
  const [activeFlowIcon, setActiveFlowIcon] = useState<string | null>(null);
  const [openSettings, setOpenSettings] = useState<(() => void) | null>(null);
  useEffect(() => {
    if (!inEditor) {
      setActiveFlowName(null);
      setActiveFlowIcon(null);
      setOpenSettings(null);
    }
  }, [inEditor]);

  const toggleNav = () =>
    setNavCollapsed((x) => {
      const next = !x;
      if (!isNarrower(MOBILE)) {
        try {
          localStorage.setItem(COLLAPSE_KEY, next ? "1" : "0");
        } catch {
          /* localStorage might be blocked in a strict-mode iframe */
        }
      }
      return next;
    });
  const closeNav = () => setNavCollapsed(true);

  // In the editor the open flow takes this slot.
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
      <div
        className="app-shell"
        data-nav-collapsed={navCollapsed ? "true" : "false"}
      >
        <header className="topbar">
          <Button
            variant="ghost"
            size="icon"
            className="hamburger"
            onClick={toggleNav}
            aria-label={t("nav.toggleNav")}
            aria-expanded={!navCollapsed}
          >
            {/* Custom 3-bar burger: the bottom bar is shorter (the icon
              variant we want), and the whole mark morphs into a cross
              when the drawer is open (aria-expanded="true"). */}
            <span className="burger" aria-hidden="true">
              <span className="burger-bar burger-top" />
              <span className="burger-bar burger-mid" />
              <span className="burger-bar burger-bot" />
            </span>
          </Button>
          {/* The logo is the home affordance — clicking it lands on the
            start/welcome screen (where no flow is selected), matching
            the sibling `dazy` app's brand-click-goes-home behaviour. */}
          <NavLink
            to="/welcome"
            className="brand"
            title={orgName || "Dazyflow"}
          >
            {/* Mark: the open flow's icon in the editor, else the org logo
              when set, else the Dazyflow favicon. */}
            {showFlow && activeFlowIcon ? (
              <FlowIcon icon={activeFlowIcon} size={22} />
            ) : !showFlow && isImageIcon(orgIcon) ? (
              <img
                src={orgIcon}
                alt=""
                className="flow-icon-img"
                width={24}
                height={24}
                draggable={false}
              />
            ) : (
              <img
                src="/logo.svg"
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
              {showFlow ? activeFlowName : orgName || "Dazyflow"}
            </span>
          </NavLink>
          <div className="spacer" />
          {me && (
            <div className="user">
              {/* Help (also on "?"): docs, support, keyboard shortcuts. Stays
                visible on mobile now that it carries more than accelerators —
                the docs and support links are exactly what a stuck user on a
                phone needs. */}
              <Button
                variant="ghost"
                size="icon"
                onClick={() => setHelpOpen(true)}
                aria-label={t("help.title")}
                title={t("help.title")}
              >
                <HelpCircle size={ICON.lg} />
              </Button>
              {/* Org switcher: opens a modal listing the orgs you can act in,
                with an inline "Create organization" form. Always available so
                even a single-tenant user can spin up a new org. */}
              <Button
                variant="ghost"
                className="org-switcher-trigger"
                onClick={() => setOrgModalOpen(true)}
                title={t("nav.switchTenant")}
              >
                {orgGlyph(
                  me.memberships?.find(
                    (m) => m.tenant === (activeTenant || me.tenant),
                  )?.icon,
                  14,
                )}
                <strong>
                  {tenantDisplayName(
                    me,
                    activeTenant || me.tenant,
                    t("nav.personalWorkspace"),
                  )}
                </strong>
                <ChevronDown size={ICON.xs} />
              </Button>
              {inEditor && openSettings && (
                <FlowMenu onOpenSettings={openSettings} />
              )}
            </div>
          )}
          {me &&
            orgModalOpen &&
            (() => {
              const active = activeTenant || me.tenant;
              const homeTenant =
                me.memberships?.find((m) => m.home)?.tenant ?? me.tenant;
              const isPlatformAdmin = me.permissions.includes("platform:admin");
              const isOrgAdmin = (tid: string) =>
                me.memberships
                  ?.find((m) => m.tenant === tid)
                  ?.roles.some((r) =>
                    r.permissions.includes("organization:admin"),
                  ) ?? false;
              const deletable = (tid: string) =>
                tid !== homeTenant &&
                (isPlatformAdmin || (tid === active && isOrgAdmin(tid)));
              return (
                <OrgSwitcherModal
                  orgs={tenants.map((tid) => ({
                    tenant: tid,
                    name: tenantDisplayName(
                      me,
                      tid,
                      t("nav.personalWorkspace"),
                    ),
                    glyph: orgGlyph(
                      me.memberships?.find((m) => m.tenant === tid)?.icon,
                      18,
                    ),
                    deletable: deletable(tid),
                  }))}
                  activeTenant={active}
                  showId={shouldShowTenantID(me, tenants.length)}
                  onPick={(tid) => {
                    setOrgModalOpen(false);
                    // A deep reload, or the active page keeps rendering the previous org's data.
                    if (tid !== active) setActiveTenant(tid, { reload: true });
                  }}
                  onCreate={async (displayName) => {
                    if (!token) return;
                    const res = await api.createOrg(token, displayName);
                    setOrgModalOpen(false);
                    setActiveTenant(res.tenant, { reload: true });
                  }}
                  onExport={async (tid) => {
                    if (!token) return;
                    const data = await api.exportOrg(token, tid);
                    downloadJson(data, `dazyflow-org-${tid}-export.json`);
                  }}
                  onDelete={async (tid, password) => {
                    if (!token) return;
                    await api.deleteOrg(token, tid, password);
                    if (tid === active) setActiveTenant(homeTenant);
                    reloadTenants();
                    setOrgModalOpen(false);
                  }}
                  onClose={() => setOrgModalOpen(false)}
                />
              );
            })()}
        </header>
        <div className="body">
          <aside
            className="sidebar"
            data-collapsed={navCollapsed ? "true" : "false"}
          >
            <div className="group-label">{t("nav.workspaceGroup")}</div>
            {/* Primary create CTA — the single entry point into the unified
              "Create flow" surface (blank / AI / template). Replaces the
              old standalone Templates nav entry. Collapses to icon-only in
              the rail like the other entries. */}
            <NavLink
              to="/flows/new"
              className="sidebar-new-flow"
              title={t("flowList.newFlow")}
            >
              <Plus size={ICON.lg} />
              <span className="nav-label">{t("flowList.newFlow")}</span>
            </NavLink>
            <NavLink to="/overview" title={t("nav.overview")}>
              <Gauge size={ICON.lg} />
              <span className="nav-label">{t("nav.overview")}</span>
            </NavLink>
            <NavLink to="/flows" title={t("common.flows")}>
              <Workflow size={ICON.lg} />
              <span className="nav-label">{t("common.flows")}</span>
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
                    <FlowIcon
                      icon={f.icon}
                      size={ICON.sm}
                      className="nav-flow-icon"
                    />
                    <span className="nav-label nav-flow-name">
                      {f.name || f.id}
                    </span>
                    {f.published === false && (
                      <PencilLine
                        size={ICON.sm}
                        className="nav-flow-draft"
                        aria-label={t("nav.draft")}
                      />
                    )}
                  </NavLink>
                ))}
              </div>
            )}
            <NavLink to="/runs" title={t("nav.runs")}>
              <Activity size={ICON.lg} />
              <span className="nav-label">{t("nav.runs")}</span>
            </NavLink>
            {/* Results — the in-app view of data flows saved to the Built-in
              store. A zero-config "see what my flow produced" surface. */}
            <NavLink to="/collections" title={t("nav.results")}>
              <Table2 size={ICON.lg} />
              <span className="nav-label">{t("nav.results")}</span>
            </NavLink>
            {/* Files is an authoring surface (workspace inputs/outputs), gated to
              editors/admins like Secrets — viewers (graph:run only) don't see
              it and can't browse/download. */}
            {hasPerm("graph:edit") && (
              <NavLink to="/files" title={t("nav.files")}>
                <FolderTree size={ICON.lg} />
                <span className="nav-label">{t("nav.files")}</span>
              </NavLink>
            )}
            {/* Approvals lives in the sidebar only when it's actually
              useful: any pending approval right now, a sticky flag
              from a previous visit, or an admin who needs to know
              one might exist. Non-tech buyers whose flows don't use
              await_approval never see the link, removing a confusing
              "what's this?" entry from their default sidebar. */}
            {(pendingCount > 0 ||
              everHadApproval ||
              hasPerm("organization:admin")) && (
              <NavLink to="/approvals" title={t("nav.approvals")}>
                <Inbox size={ICON.lg} />
                <span className="nav-label" style={{ flex: 1 }}>
                  {t("nav.approvals")}
                </span>
                {pendingCount > 0 && (
                  <span className="nav-badge">{pendingCount}</span>
                )}
              </NavLink>
            )}
            {/* Apps is the integration catalog (connect Slack/Gmail, see
              what's ready to use). Viewable by everyone. Org secret values
              live under Admin → Secrets. */}
            <NavLink to="/apps" title={t("nav.apps")}>
              <Boxes size={ICON.lg} />
              <span className="nav-label">{t("nav.apps")}</span>
            </NavLink>
            {/* Support tickets: only when the deployment wired the native
              ticket surface. A support agent instead gets the cross-tenant
              queue at the same entry point. */}
            {supportOn && (
              <NavLink
                to={isAgent ? "/support/queue" : "/support"}
                title={t("nav.support")}
              >
                <LifeBuoy size={ICON.lg} />
                <span className="nav-label" style={{ flex: 1 }}>
                  {t("nav.support")}
                </span>
                {supportUnread > 0 && (
                  <span className="nav-badge">{supportUnread}</span>
                )}
              </NavLink>
            )}
            <div className="sidebar-spacer" />
            {/* Classical collapse arrow. Duplicates the hamburger's
              collapse action so the affordance sits next to the panel it
              controls — the standard pattern in VS Code, Linear, etc.
              Hidden on mobile where the hamburger drives a slide-over. */}
            <Button
              className="sidebar-collapse-toggle"
              onClick={toggleNav}
              aria-label={
                navCollapsed ? t("nav.expandSidebar") : t("nav.collapseSidebar")
              }
              title={
                navCollapsed ? t("nav.expandSidebar") : t("nav.collapseSidebar")
              }
            >
              {navCollapsed ? (
                <ChevronRight size={ICON.md} />
              ) : (
                <ChevronLeft size={ICON.md} />
              )}
              <span className="nav-label">
                {navCollapsed
                  ? t("nav.expandSidebar")
                  : t("nav.collapseSidebar")}
              </span>
            </Button>
            {/* Account control pinned to the very bottom of the sidebar —
              the entry point to per-user actions (Settings, Sign out).
              Mirrors the sibling `dazy` app's sidebar-footer account
              menu. Shows just the user icon in the collapsed rail. */}
            {me && (
              <AccountMenu
                email={me.subject || t("nav.noSubject")}
                onSignOut={signOut}
                onConnectMcp={() => setConnectingMcp(true)}
                collapsed={navCollapsed}
                showAdmin={showAdmin}
                version={serverVersion}
              />
            )}
          </aside>
          {/* Scrim behind the mobile drawer — tap to close. Inert on
            desktop (CSS hides it); only interactive on small screens
            while the drawer is open. */}
          <Button
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
            {/* Gentle cross-page fade: keying on the path remounts this wrapper on
              navigation, re-triggering the .route-fade entry animation. Skipped
              in the editor — the canvas shouldn't fade/remount on every change. */}
            {inEditor ? (
              children
            ) : (
              <div key={location.pathname} className="route-fade">
                {children}
              </div>
            )}
          </main>
        </div>
        <CommandPalette
          open={cmdOpen}
          onClose={() => setCmdOpen(false)}
          flows={flows}
        />
        {helpOpen && <HelpModal onClose={() => setHelpOpen(false)} />}
        {connectingMcp && (
          <ConnectMcpClientModal onClose={() => setConnectingMcp(false)} />
        )}
      </div>
    </ActiveFlowContext.Provider>
  );
}

function AccountMenu({
  email,
  onSignOut,
  onConnectMcp,
  collapsed,
  showAdmin,
  version,
}: {
  email: string;
  onSignOut: () => void;
  onConnectMcp: () => void;
  collapsed: boolean;
  showAdmin: boolean;
  version: string | null;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { token, activeTenant } = useAuth();
  const [open, setOpen] = useState(false);
  const [canUpgrade, setCanUpgrade] = useState(false);
  const [billingEnabled, setBillingEnabled] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  // Portaled to body, so the sidebar's overflow cannot clip it.
  const [pos, setPos] = useState<{ left: number; bottom: number } | null>(null);
  const place = () => {
    const r = triggerRef.current?.getBoundingClientRect();
    if (!r) return;
    setPos({ left: r.left, bottom: window.innerHeight - r.top + 6 });
  };
  useEffect(() => {
    if (!token) return;
    let live = true;
    api
      .getBilling(token, activeTenant || undefined)
      .then((b) => {
        if (!live) return;
        setCanUpgrade(!!b?.can_upgrade);
        setBillingEnabled(!!b?.billing_enabled);
      })
      .catch(() => {
        if (!live) return;
        setCanUpgrade(false);
        setBillingEnabled(false);
      });
    return () => {
      live = false;
    };
  }, [token, activeTenant]);

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
      <Button
        ref={triggerRef}
        className="account-menu-trigger"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("account.menu")}
        title={collapsed ? email : undefined}
      >
        <SettingsIcon size={ICON.lg} />
        <span className="nav-label account-email">{email}</span>
        <ChevronDown size={ICON.sm} className="nav-label" />
      </Button>
      {open &&
        pos &&
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
            {/* Upgrade CTA — only on a free plan with Stripe configured. Sits
                at the top, accented, as the menu's most prominent action. */}
            {canUpgrade && (
              <>
                <Button
                  role="menuitem"
                  className="workspace-pop-row account-pop-row account-pop-upgrade"
                  onClick={() => {
                    setOpen(false);
                    navigate("/usage");
                  }}
                >
                  <Sparkles size={ICON.sm} />
                  {t("nav.upgrade")}
                </Button>
                <div className="workspace-pop-sep" role="separator" />
              </>
            )}
            <Button
              role="menuitem"
              className="workspace-pop-row account-pop-row"
              onClick={() => {
                setOpen(false);
                navigate("/settings");
              }}
            >
              <SettingsIcon size={ICON.sm} />
              {t("account.settings")}
            </Button>
            {/* Connect an assistant — the guided MCP-client flow: mints a
                personal, workspace-scoped API key and hands back the
                config snippet for Claude Desktop / Claude Code. Lives here
                as a per-user action next to Settings (the old Welcome-page
                entry point was dropped in "Simplify welcome"). */}
            <Button
              role="menuitem"
              className="workspace-pop-row account-pop-row"
              onClick={() => {
                setOpen(false);
                onConnectMcp();
              }}
            >
              <Plug size={ICON.sm} />
              {t("account.connectMcp")}
            </Button>
            {/* Plan & usage (plan, billing, consumption) — one account-menu
                entry for the merged page. It's billing/account info, not a
                workspace surface a first-time user needs in their face. On a
                self-host without billing it's pure usage metering, so the label
                drops the "Plan &" framing. */}
            <Button
              role="menuitem"
              className="workspace-pop-row account-pop-row"
              onClick={() => {
                setOpen(false);
                navigate("/usage");
              }}
            >
              <CreditCard size={ICON.sm} />
              {billingEnabled ? t("nav.plans") : t("common.usage")}
            </Button>
            {showAdmin && (
              <Button
                role="menuitem"
                className="workspace-pop-row account-pop-row"
                onClick={() => {
                  setOpen(false);
                  navigate("/admin");
                }}
              >
                <ShieldCheck size={ICON.sm} />
                {t("nav.admin")}
              </Button>
            )}
            <div className="workspace-pop-sep" role="separator" />
            <Button
              variant="danger"
              role="menuitem"
              className="workspace-pop-row account-pop-row"
              onClick={() => {
                setOpen(false);
                onSignOut();
              }}
            >
              <LogOut size={ICON.sm} />
              {t("account.signOut")}
            </Button>
            {/* Version footer — informational, not a menu item. Hidden
                until the GET /api/v1 fetch resolves. "dev" on an
                unstamped local build; a release shows e.g. "v0.1.0". */}
            {version && (
              <>
                <div className="workspace-pop-sep" role="separator" />
                <div className="account-pop-version">v{version}</div>
              </>
            )}
          </div>,
          document.body,
        )}
    </div>
  );
}

function FlowMenu({ onOpenSettings }: { onOpenSettings: () => void }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const { trigger, pop, style } = useAnchoredPop<
    HTMLButtonElement,
    HTMLDivElement
  >(open);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (!target.closest(".flow-menu") && !target.closest(".flow-menu-pop")) {
        setOpen(false);
      }
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
    <div className="flow-menu">
      <Button
        ref={trigger}
        variant="ghost"
        size="icon"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("flowMenu.label")}
        title={t("flowMenu.label")}
      >
        <MoreVertical size={ICON.lg} />
      </Button>
      {open &&
        createPortal(
          <div
            className="workspace-pop account-pop flow-menu-pop"
            role="menu"
            ref={pop}
            style={style}
          >
            <Button
              role="menuitem"
              className="workspace-pop-row account-pop-row"
              onClick={() => {
                setOpen(false);
                onOpenSettings();
              }}
            >
              <SettingsIcon size={ICON.sm} />
              {t("flowMenu.settings")}
            </Button>
          </div>,
          document.body,
        )}
    </div>
  );
}

function VerifyEmailBanner() {
  const { t } = useTranslation();
  const { token, refreshMe } = useAuth();
  const [sent, setSent] = useState(false);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [hidden, setHidden] = useState(false);

  if (hidden) return null;
  const resend = async () => {
    if (!token) return;
    setBusy(true);
    setFailed(false);
    let res: { sent?: boolean; already_verified?: boolean } | null = null;
    try {
      res = await api.resendVerification(token);
    } catch {
      res = null; // the mailer is down; the route answers 502
    }
    switch (resendOutcome(res)) {
      case "verified":
        await refreshMe(); // banner disappears via whoami
        break;
      case "sent":
        setSent(true);
        break;
      case "failed":
        setFailed(true);
        break;
    }
    setBusy(false);
  };
  return (
    <div className="card verify-banner">
      <MailWarning size={ICON.lg} className="verify-banner-icon" />
      <div className="verify-banner-body" role={failed ? "alert" : undefined}>
        {sent
          ? t("verifyEmail.bannerSent")
          : failed
            ? t("verifyEmail.bannerFailed")
            : t("verifyEmail.banner")}
      </div>
      {!sent && (
        <Button
          className="welcome-cta verify-banner-resend"
          disabled={busy}
          onClick={() => void resend()}
        >
          <Send size={ICON.sm} />
          {busy
            ? t("common.sending")
            : t(
                failed ? "verifyEmail.bannerRetry" : "verifyEmail.bannerResend",
              )}
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon"
        onClick={() => setHidden(true)}
        aria-label={t("verifyEmail.bannerDismiss")}
        title={t("verifyEmail.bannerDismiss")}
      >
        <X size={ICON.sm} />
      </Button>
    </div>
  );
}
