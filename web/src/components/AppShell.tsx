// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
import { Button } from "./Button";
import { OrgSwitcherModal } from "./OrgSwitcherModal";
import { ConnectMcpClientModal } from "./ConnectMcpClientModal";
import { CommandPalette } from "./CommandPalette";
import { HelpModal } from "./HelpModal";
import { FlowIcon } from "../icons";
import { isImageIcon } from "../lib/iconImage";
import type { FlowSummary } from "../types";

// orgGlyph renders an org's icon: an uploaded image (data: URL) as an
// <img>, otherwise the generic Building2 mark. Shared by the tenant
// switcher trigger + rows.
// downloadJson saves an object as a pretty-printed .json file via a transient
// blob URL — used for the org export (the fetch carries the auth token, so a
// plain <a href> to the endpoint wouldn't work).
function downloadJson(data: unknown, filename: string) {
  const blob = new Blob([JSON.stringify(data, null, 2)], {
    type: "application/json",
  });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

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
const COLLAPSE_KEY = "dazyflow.sidebar.collapsed";
// APPROVAL_SEEN_KEY is the sticky local flag the Approvals nav link
// uses to stay visible after the user has used the approval inbox
// at least once. See the everHadApproval state below.
const APPROVAL_SEEN_KEY = "dazyflow.approvalsEverSeen";
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
    activeWorkspace,
    tenants,
    activeTenant,
    setActiveTenant,
    reloadTenants,
  } = useAuth();
  const [orgModalOpen, setOrgModalOpen] = useState(false);
  // Drives the "Connect an assistant" guided modal (mints a personal,
  // workspace-scoped API key + hands back the MCP client config). Opened
  // from the account menu; the modal pulls auth context itself.
  const [connectingMcp, setConnectingMcp] = useState(false);
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
  // supportUnread badges the Support nav entry: for a user, tickets now waiting
  // on them (support replied); for an agent, tickets waiting on support. Closes
  // the loop — otherwise a reply is invisible until the user thinks to look.
  const [supportUnread, setSupportUnread] = useState(0);
  // All flows in the active workspace, listed under the "Flows" nav entry.
  const [flows, setFlows] = useState<FlowSummary[]>([]);
  // Global ⌘K command bar (jump to any page/flow). Mounted app-wide but only
  // bound to ⌘K OUTSIDE the flow editor, where ⌘K stays "Add step".
  const [cmdOpen, setCmdOpen] = useState(false);
  // Help (header "?" button + "?" key): docs, support, keyboard shortcuts.
  const [helpOpen, setHelpOpen] = useState(false);
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
  // Support unread count: agents count queue tickets awaiting support; users
  // count their own tickets awaiting them. Polled slowly (60s) — it only drives
  // a badge. Skipped entirely when the ticket surface is off.
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
        ? api.listTicketQueue(token, { status: "awaiting_support" })
        : api.listMyTickets(token, "awaiting_user");
      p.then((r) => {
        if (!cancelled) setSupportUnread(r.tickets?.length ?? 0);
      }).catch(() => {
        /* ignore — non-essential */
      });
    };
    fetch();
    const iv = window.setInterval(fetch, 60000);
    return () => {
      cancelled = true;
      window.clearInterval(iv);
    };
  }, [token, supportOn, isAgent, location.pathname]);
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
  // Editor pages need a full-bleed canvas — remove the main padding. Match the
  // canonical /flows/:id or the legacy /pipelines/:id path so an incoming
  // legacy link still gets the right layout during the one-render redirect.
  // EXCLUDE /flows/new — that's the Create-flow page, a normal padded page, not
  // an editor canvas (without the exclusion it matches :id and loses its margins).
  // The support-agent flow view is a full-bleed read-only canvas too, so it
  // wants the same padding-free main as the editor.
  const inEditor =
    /^\/(flows|pipelines)\/(?!new(?:$|\/))[^/]+/.test(location.pathname) ||
    /^\/support\/flows\//.test(location.pathname);
  const showAdmin =
    hasPerm("organization:admin") || hasPerm("graph:admin");

  // ⌘K / Ctrl+K opens the global command bar — except in the flow editor,
  // which owns ⌘K for its step palette (per the agreed keybinding split).
  // "?" opens the keyboard-shortcuts reference (ignored while typing).
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
  // profile set; otherwise it's the Dazyflow mark.
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
        <NavLink to="/welcome" className="brand" title={orgName || "Dazyflow"}>
          {/* Mark: the open flow's icon in the editor, else the org logo
              when set, else the Dazyflow favicon. */}
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
              className="shortcuts-help"
              onClick={() => setHelpOpen(true)}
              aria-label={t("help.title")}
              title={t("help.title")}
            >
              <HelpCircle size={18} />
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
                me.memberships?.find((m) => m.tenant === (activeTenant || me.tenant))
                  ?.icon,
                14,
              )}
              <strong>
                {tenantDisplayName(
                  me,
                  activeTenant || me.tenant,
                  t("nav.personalWorkspace"),
                )}
              </strong>
              <ChevronDown size={12} />
            </Button>
            {inEditor && openSettings && (
              <FlowMenu onOpenSettings={openSettings} />
            )}
          </div>
        )}
        {me && orgModalOpen && (() => {
          const active = activeTenant || me.tenant;
          const homeTenant =
            me.memberships?.find((m) => m.home)?.tenant ?? me.tenant;
          const isPlatformAdmin = me.permissions.includes("platform:admin");
          const isOrgAdmin = (tid: string) =>
            me.memberships
              ?.find((m) => m.tenant === tid)
              ?.roles.some((r) => r.permissions.includes("organization:admin")) ??
            false;
          // Deletable when it isn't your home org AND you're either a platform
          // admin (any org) or an org admin of the org you're currently in
          // (the daemon requires p.Tenant == tenant for non-platform deletes).
          const deletable = (tid: string) =>
            tid !== homeTenant &&
            (isPlatformAdmin || (tid === active && isOrgAdmin(tid)));
          return (
            <OrgSwitcherModal
              orgs={tenants.map((tid) => ({
                tenant: tid,
                name: tenantDisplayName(me, tid, t("nav.personalWorkspace")),
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
                // Switching org deep-reloads the app so the active page can't
                // keep showing the previous org's data. No-op if it's already
                // the active org.
                if (tid !== active) setActiveTenant(tid, { reload: true });
              }}
              onCreate={async (displayName) => {
                if (!token) return;
                const res = await api.createOrg(token, displayName);
                // Switch into the new org with a deep reload: the cold boot
                // rebuilds the tenant catalogue (so it appears) and refetches
                // in the new scope, so no page keeps the previous org's data.
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
                // Leave the org if it was the active one, then refresh.
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
            <Plus size={18} />
            <span className="nav-label">{t("flowList.newFlow")}</span>
          </NavLink>
          <NavLink to="/overview" title={t("nav.overview")}>
            <Gauge size={18} />
            <span className="nav-label">{t("nav.overview")}</span>
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
                  {f.published === false && (
                    <PencilLine
                      size={13}
                      className="nav-flow-draft"
                      aria-label={t("nav.draft")}
                    />
                  )}
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
          {/* Results — the in-app view of data flows saved to the Built-in
              store. A zero-config "see what my flow produced" surface. */}
          <NavLink
            to="/collections"
            title={t("nav.results")}
          >
            <Table2 size={18} />
            <span className="nav-label">{t("nav.results")}</span>
          </NavLink>
          {/* Files is an authoring surface (workspace inputs/outputs), gated to
              editors/admins like Secrets — viewers (graph:run only) don't see
              it and can't browse/download. */}
          {hasPerm("graph:edit") && (
            <NavLink
              to="/files"
              title={t("nav.files")}
            >
              <FolderTree size={18} />
              <span className="nav-label">{t("nav.files")}</span>
            </NavLink>
          )}
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
          {/* Apps is the integration catalog (connect Slack/Gmail, see
              what's ready to use). Viewable by everyone. Org secret values
              live under Admin → Secrets. */}
          <NavLink
            to="/apps"
            title={t("nav.apps")}
          >
            <Boxes size={18} />
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
              <LifeBuoy size={18} />
              <span className="nav-label" style={{ flex: 1 }}>{t("nav.support")}</span>
              {supportUnread > 0 && <span className="nav-badge">{supportUnread}</span>}
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

// AccountMenu is the lower-left sidebar account control — the entry
// point to per-user actions (Settings, Sign out). Modelled on the
// sibling `dazy` app, whose account/settings menu lives in the sidebar
// footer. The trigger shows a user icon + email (the email hides via
// .nav-label in the collapsed rail, leaving just the icon); the menu
// pops upward so it doesn't run off the bottom of the viewport.
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
  // Whether to show the "Upgrade to Pro" CTA: true only on a free plan where
  // Stripe upgrades are configured. Best-effort and server-cached; a failed or
  // unconfigured billing lookup simply hides the CTA.
  const [canUpgrade, setCanUpgrade] = useState(false);
  // Whether this deployment runs paid billing at all. When false (a self-host
  // without Stripe), the account entry reads "Usage" not "Plan & usage".
  const [billingEnabled, setBillingEnabled] = useState(false);
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
      <Button
        ref={triggerRef}
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
      </Button>
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
                  <Sparkles size={14} />
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
              <SettingsIcon size={14} />
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
              <Plug size={14} />
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
              <CreditCard size={14} />
              {billingEnabled ? t("nav.plans") : t("nav.usage")}
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
                <ShieldCheck size={14} />
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
              <LogOut size={14} />
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

// FlowMenu is the top-right three-dots menu acting on the current flow
// (mirrors dazy's per-context menu). Today it has one action — open the
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
      <Button
        variant="ghost"
        size="icon"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t("flowMenu.label")}
        title={t("flowMenu.label")}
      >
        <MoreVertical size={18} />
      </Button>
      {open && (
        <div className="workspace-pop account-pop" role="menu">
          <Button
            role="menuitem"
            className="workspace-pop-row account-pop-row"
            onClick={() => {
              setOpen(false);
              onOpenSettings();
            }}
          >
            <SettingsIcon size={14} />
            {t("flowMenu.settings")}
          </Button>
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
    <div className="card verify-banner">
      <MailWarning size={18} className="verify-banner-icon" />
      <div className="verify-banner-body">
        {sent ? t("verifyEmail.bannerSent") : t("verifyEmail.banner")}
      </div>
      {!sent && (
        <Button
          className="welcome-cta verify-banner-resend"
          disabled={busy}
          onClick={() => void resend()}
        >
          <Send size={14} />
          {busy ? t("verifyEmail.bannerSending") : t("verifyEmail.bannerResend")}
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon"
        className="verify-banner-dismiss"
        onClick={() => setHidden(true)}
        aria-label={t("verifyEmail.bannerDismiss")}
        title={t("verifyEmail.bannerDismiss")}
      >
        <X size={14} />
      </Button>
    </div>
  );
}
