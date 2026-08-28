// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import {
  Navigate,
  Route,
  Routes,
  useLocation,
  useParams,
} from "react-router-dom";
import { useAuth } from "./auth";
import { api } from "./api";
import { userScope } from "./recentFlow";
import { AppShell } from "./components/AppShell";
import { SignIn } from "./pages/auth/SignIn";
import { SignUp } from "./pages/auth/SignUp";
import { Welcome } from "./pages/Welcome";
import { NotFound } from "./pages/NotFound";
import { Dashboard } from "./pages/Dashboard";
import { CreateFlow } from "./pages/flows/CreateFlow";
import { Apps, AppDetail } from "./pages/Apps";
import { Files } from "./pages/Files";
import { FlowList } from "./pages/flows/FlowList";
import { FlowEditor } from "./pages/flows/FlowEditor";
import { RunList } from "./pages/runs/RunList";
import { RunDetail } from "./pages/runs/RunDetail";
import { Results } from "./pages/Results";
import { Approvals } from "./pages/runs/Approvals";
import { Usage } from "./pages/Usage";
import { VerifyEmail } from "./pages/auth/VerifyEmail";
import { ForgotPassword } from "./pages/auth/ForgotPassword";
import { ResetPassword } from "./pages/auth/ResetPassword";
import { Settings } from "./pages/Settings";
import { Admin } from "./pages/admin/Admin";
import { AdminAPIKeys } from "./pages/admin/AdminAPIKeys";
import { AdminUsers } from "./pages/admin/AdminUsers";
import { AdminSupport } from "./pages/admin/AdminSupport";
import { SupportAgentHome } from "./pages/support/SupportAgentHome";
import { SupportFlowView } from "./pages/support/SupportFlowView";
import { SupportTickets, SupportQueue, TicketThread } from "./pages/support/SupportTickets";
import { AdminAudit } from "./pages/admin/AdminAudit";
import { AdminWorkspace } from "./pages/admin/AdminWorkspace";
import { AdminOrgSSO } from "./pages/admin/AdminOrgSSO";
import { AdminOAuthProviders } from "./pages/admin/AdminOAuthProviders";
import { AdminPlatform } from "./pages/admin/platform/AdminPlatform";
import { AdminPlatformDrops } from "./pages/admin/platform/AdminPlatformDrops";
import { AdminPlatformUsers } from "./pages/admin/platform/AdminPlatformUsers";
import { AdminPlatformUserDetail } from "./pages/admin/platform/AdminPlatformUserDetail";
import { AdminPlatformOrgs } from "./pages/admin/platform/AdminPlatformOrgs";
import { AdminPlatformOrgDetail } from "./pages/admin/platform/AdminPlatformOrgDetail";
import { AdminPlatformTiers } from "./pages/admin/platform/AdminPlatformTiers";
import { AdminPlatformSupportAgents } from "./pages/admin/platform/AdminPlatformSupportAgents";
import { AdminSystemLog } from "./pages/admin/AdminSystemLog";
import { AdminGoogle } from "./pages/admin/AdminGoogle";
import { AdminSecrets } from "./pages/admin/AdminSecrets";
import { EmailTemplates } from "./pages/admin/EmailTemplates";
import { AdminGitCredentials } from "./pages/admin/AdminGitCredentials";
import { AdminRunners } from "./pages/admin/AdminRunners";
import { AdminMCPServers } from "./pages/admin/AdminMCPServers";
import { AdminWebAPIs } from "./pages/admin/AdminWebAPIs";
import { AdminRunnerDetail } from "./pages/admin/AdminRunnerDetail";
import { AcceptInvite } from "./pages/auth/AcceptInvite";
import { PublicOverview } from "./pages/PublicOverview";
import { UploadsProvider } from "./uploads";

export function App() {
  const { token } = useAuth();
  const { pathname } = useLocation();
  // Public TV-dashboard share page: cryptic link, no auth, no AppShell.
  // Handled before the signed-in/out split so it renders identically whether
  // or not a session exists (an operator previewing it, or a login-less TV).
  if (pathname.startsWith("/tv/")) {
    return (
      <Routes>
        <Route path="/tv/:token" element={<PublicOverview />} />
      </Routes>
    );
  }
  if (!token) {
    // Render the unauthenticated routes even while auth is "loading". A
    // sign-in / sign-up / TOTP attempt sets loading=true with no token yet,
    // so blanking the tree here would unmount <SignIn> mid-request — wiping
    // its local state (the TOTP code step never appears) and re-running its
    // mount effects (a just-set "wrong password" error gets cleared). The
    // bootstrap "validating a stored token" case is loading && token, which
    // already renders the authenticated tree below.
    // Unauthenticated: signin/signup are reachable as deep-links;
    // /invite/<token> renders the public invite landing (the recipient
    // can read who invited them before signing in). Anything else
    // 302s into signin.
    return (
      <Routes>
        <Route path="/signup" element={<SignUp />} />
        <Route path="/signin" element={<SignIn />} />
        {/* The verification link can land in a signed-out browser —
            the token is the proof, no session needed. */}
        <Route path="/verify-email" element={<VerifyEmail />} />
        <Route path="/forgot-password" element={<ForgotPassword />} />
        <Route path="/reset-password" element={<ResetPassword />} />
        <Route path="/invite/:token" element={<AcceptInvite />} />
        {/* Signed-out catch-all. Tell the visitor the page wasn't found —
            landing on a bare sign-in form reads as "you were logged out",
            which is a different and more alarming thing than a bad link. */}
        <Route path="*" element={<SignIn notFound />} />
      </Routes>
    );
  }
  return (
    <UploadsProvider>
    <AppShell>
      <Routes>
        <Route path="/" element={<RootRedirect />} />
        <Route path="/welcome" element={<Welcome />} />
        <Route path="/overview" element={<Dashboard />} />
        <Route path="/flows" element={<FlowList />} />
        {/* Static "new" is listed before the :id param route. The unified
            creation surface (blank / AI / template tabs) lives here. */}
        <Route path="/flows/new" element={<CreateFlow />} />
        <Route path="/flows/:id" element={<KeyedFlowEditor />} />
        {/* Templates folded into the create page — keep the old path working
            for bookmarks/deep-links by redirecting onto the template tab. */}
        <Route
          path="/templates"
          element={<Navigate to="/flows/new?tab=template" replace />}
        />
        {/* Apps is the integration catalog; /apps/:slug is one app's detail. */}
        <Route path="/apps" element={<Apps />} />
        <Route path="/apps/:slug" element={<AppDetail />} />
        <Route path="/files" element={<Files />} />
        <Route path="/runs" element={<RunList />} />
        {/* Collections: the nav label and the page title both say
            "Collections", so the URL does too. /results was the old path —
            keep it as a redirect for bookmarks, same as /templates and
            /plans below. */}
        <Route path="/collections" element={<Results />} />
        <Route path="/results" element={<Navigate to="/collections" replace />} />
        <Route path="/runs/:runID" element={<RunDetail />} />
        <Route path="/approvals" element={<Approvals />} />
        {/* /support is role-sensitive: a support agent lands on their
            flow-view home (grant-gated redacted views), a regular user on
            their own tickets. The agent ticket queue lives under /queue; the
            grant-gated flow view keeps its own deep path. */}
        <Route path="/support" element={<SupportRoot />} />
        <Route path="/support/queue" element={<SupportQueue />} />
        <Route path="/support/queue/:id" element={<TicketThread mode="agent" />} />
        <Route path="/support/flows/:tenant/:workspace/:flowId" element={<SupportFlowView />} />
        <Route path="/support/:id" element={<TicketThread mode="user" />} />
        <Route path="/usage" element={<Usage />} />
        {/* /plans folded into the merged Plan & usage page; keep the path
            as a redirect so old links and the account menu still resolve. */}
        <Route path="/plans" element={<Navigate to="/usage" replace />} />
        <Route path="/verify-email" element={<VerifyEmail />} />
        <Route path="/reset-password" element={<ResetPassword />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/admin" element={<Admin />} />
        <Route path="/admin/api-keys" element={<AdminAPIKeys />} />
        <Route path="/admin/users" element={<AdminUsers />} />
        <Route path="/admin/support" element={<AdminSupport />} />
        <Route path="/admin/audit" element={<AdminAudit />} />
        <Route path="/admin/workspace" element={<AdminWorkspace />} />
        <Route path="/admin/sso" element={<AdminOrgSSO />} />
        <Route path="/admin/oauth" element={<AdminOAuthProviders />} />
        <Route path="/admin/platform" element={<AdminPlatform />} />
        <Route path="/admin/platform/drops" element={<AdminPlatformDrops />} />
        <Route path="/admin/platform/users" element={<AdminPlatformUsers />} />
        <Route path="/admin/platform/users/:email" element={<AdminPlatformUserDetail />} />
        <Route path="/admin/platform/orgs" element={<AdminPlatformOrgs />} />
        <Route path="/admin/platform/orgs/:tenant" element={<AdminPlatformOrgDetail />} />
        <Route path="/admin/platform/tiers" element={<AdminPlatformTiers />} />
        <Route path="/admin/platform/support-agents" element={<AdminPlatformSupportAgents />} />
        <Route path="/admin/system/log" element={<AdminSystemLog />} />
        <Route path="/admin/google" element={<AdminGoogle />} />
        <Route path="/admin/secrets" element={<AdminSecrets />} />
        <Route path="/admin/email-templates" element={<EmailTemplates />} />
        <Route path="/admin/git-credentials" element={<AdminGitCredentials />} />
        <Route path="/admin/mcp-servers" element={<AdminMCPServers />} />
        <Route path="/admin/web-apis" element={<AdminWebAPIs />} />
        <Route path="/admin/runners" element={<AdminRunners />} />
        <Route path="/admin/runners/:name" element={<AdminRunnerDetail />} />
        <Route path="/invite/:token" element={<AcceptInvite />} />
        {/* Say the page wasn't found rather than teleporting to /flows: a
            silent redirect leaves the reader unsure whether they mis-clicked
            or whether what they wanted is gone. */}
        <Route path="*" element={<NotFound />} />
      </Routes>
    </AppShell>
    </UploadsProvider>
  );
}

// HAS_FLOWS_KEY is FlowList's sticky "this user has built at least
// one flow" hint. RootRedirect reads it (and only it — no API call)
// to decide whether the bare-root visit should land on /welcome
// (first-time) or /flows (returning). Written by FlowList itself
// when the graph list resolves; cleared when it resolves empty.
const HAS_FLOWS_KEY = "dazyflow.hasFlows";

// RootRedirect decides where a logged-in user lands on the bare root.
// Three branches:
//   - A query string means "intentional deep-link" → /flows
//     (preserves ?run=… etc).
//   - A sticky localStorage flag from a previous session means the
//     user already has flows → /flows. Skips the wizard on every
//     return visit instead of forcing it.
//   - Otherwise (no flag yet), default to /welcome — the first-run
//     wizard is the right surface for someone with no flows yet.
// SupportRoot renders the right /support landing per role: a support agent sees
// their flow-view home (grant-gated redacted views); everyone else sees their
// own tickets. Keeping both at /support means upstream's back-links (Support
// FlowView → /support) and the user's ticket links all resolve unchanged.
function SupportRoot() {
  const { hasPerm } = useAuth();
  return hasPerm("support:agent") ? <SupportAgentHome /> : <SupportTickets />;
}

function RootRedirect() {
  const loc = useLocation();
  const { me, token, activeTenant, activeWorkspace } = useAuth();
  const [dest, setDest] = useState<string | null>(null);

  useEffect(() => {
    // A query string means "intentional deep-link" → /flows (preserves ?run=…).
    if (loc.search) {
      setDest(`/flows${loc.search}`);
      return;
    }
    // The flag is per-account (see userScope), so we need `me` first.
    if (!me) return;
    // Fast path: a sticky hint from a previous session on THIS origin says the
    // user already has flows → overview, no API call.
    let hasFlows = false;
    try {
      hasFlows =
        localStorage.getItem(
          `${HAS_FLOWS_KEY}.${userScope(activeTenant || me.tenant, me.subject)}`,
        ) === "1";
    } catch {
      /* private mode / strict iframe — fall through to the API check */
    }
    if (hasFlows) {
      setDest("/overview");
      return;
    }
    // No hint. localStorage is PER-ORIGIN, so a returning user arriving on a
    // fresh origin (their org subdomain, a new browser/device) has no flag and
    // must NOT be mistaken for a first-timer. Ask the server whether they
    // actually have flows, and only show the onboarding wizard when they truly
    // have none. (Seeds the hint so the next load is instant.)
    if (!token) return;
    const tenant = activeTenant || me.tenant || "";
    const workspace = activeWorkspace || me.workspace || "";
    let cancelled = false;
    api
      .listGraphs(token, tenant, workspace)
      .then((r) => {
        if (cancelled) return;
        const has = (r.graphs?.length ?? 0) > 0;
        if (has) {
          try {
            localStorage.setItem(
              `${HAS_FLOWS_KEY}.${userScope(activeTenant || me.tenant, me.subject)}`,
              "1",
            );
          } catch {
            /* ignore */
          }
        }
        setDest(has ? "/overview" : "/welcome");
      })
      .catch(() => {
        // On a lookup failure, prefer the app over re-running onboarding for
        // someone who may well have flows.
        if (!cancelled) setDest("/overview");
      });
    return () => {
      cancelled = true;
    };
  }, [loc.search, me, token, activeTenant, activeWorkspace]);

  // Render nothing until we've decided — avoids a wrong-destination flash.
  if (!dest) return <div />;
  return <Navigate to={dest} replace />;
}

// KeyedFlowEditor remounts FlowEditor whenever the :id changes. Without
// a key, react-router keeps the same FlowEditor instance mounted across
// flow→flow navigation, so it would carry the previous flow's
// currentRunID / SSE subscription / lastRun and node statuses into the
// new flow. Keying on the id forces a fresh mount per flow.
function KeyedFlowEditor() {
  const { id } = useParams();
  return <FlowEditor key={id} />;
}
