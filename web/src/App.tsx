// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Suspense, lazy, useEffect, useState } from "react";
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
import { FlowList } from "./pages/flows/FlowList";
import { VerifyEmail } from "./pages/auth/VerifyEmail";
import { ForgotPassword } from "./pages/auth/ForgotPassword";
import { ResetPassword } from "./pages/auth/ResetPassword";
import { AcceptInvite } from "./pages/auth/AcceptInvite";
import { UploadsProvider } from "./uploads";

// Split out of the initial bundle. These are the pages that carry the weight —
// the canvas (React Flow), the terminals (xterm), the map field (Leaflet), the
// Markdown renderer — plus the admin and support trees, which most sessions
// never open. Loading them on demand keeps the first paint (sign-in, the flow
// list, the dashboard) from paying for the whole app.
const FlowEditor = lazy(() =>
  import("./pages/flows/FlowEditor").then((m) => ({ default: m.FlowEditor })),
);
const CreateFlow = lazy(() =>
  import("./pages/flows/CreateFlow").then((m) => ({ default: m.CreateFlow })),
);
const Apps = lazy(() =>
  import("./pages/Apps").then((m) => ({ default: m.Apps })),
);
const AppDetail = lazy(() =>
  import("./pages/Apps").then((m) => ({ default: m.AppDetail })),
);
const Files = lazy(() =>
  import("./pages/Files").then((m) => ({ default: m.Files })),
);
const RunList = lazy(() =>
  import("./pages/runs/RunList").then((m) => ({ default: m.RunList })),
);
const RunDetail = lazy(() =>
  import("./pages/runs/RunDetail").then((m) => ({ default: m.RunDetail })),
);
const Results = lazy(() =>
  import("./pages/Results").then((m) => ({ default: m.Results })),
);
const Approvals = lazy(() =>
  import("./pages/runs/Approvals").then((m) => ({ default: m.Approvals })),
);
const Usage = lazy(() =>
  import("./pages/Usage").then((m) => ({ default: m.Usage })),
);
const Settings = lazy(() =>
  import("./pages/Settings").then((m) => ({ default: m.Settings })),
);
const SupportAgentHome = lazy(() =>
  import("./pages/support/SupportAgentHome").then((m) => ({ default: m.SupportAgentHome })),
);
const SupportFlowView = lazy(() =>
  import("./pages/support/SupportFlowView").then((m) => ({ default: m.SupportFlowView })),
);
const SupportTickets = lazy(() =>
  import("./pages/support/SupportTickets").then((m) => ({ default: m.SupportTickets })),
);
const SupportQueue = lazy(() =>
  import("./pages/support/SupportTickets").then((m) => ({ default: m.SupportQueue })),
);
const TicketThread = lazy(() =>
  import("./pages/support/SupportTickets").then((m) => ({ default: m.TicketThread })),
);
const Admin = lazy(() =>
  import("./pages/admin/Admin").then((m) => ({ default: m.Admin })),
);
const AdminAPIKeys = lazy(() =>
  import("./pages/admin/AdminAPIKeys").then((m) => ({ default: m.AdminAPIKeys })),
);
const AdminUsers = lazy(() =>
  import("./pages/admin/AdminUsers").then((m) => ({ default: m.AdminUsers })),
);
const AdminSupport = lazy(() =>
  import("./pages/admin/AdminSupport").then((m) => ({ default: m.AdminSupport })),
);
const AdminAudit = lazy(() =>
  import("./pages/admin/AdminAudit").then((m) => ({ default: m.AdminAudit })),
);
const AdminWorkspace = lazy(() =>
  import("./pages/admin/AdminWorkspace").then((m) => ({ default: m.AdminWorkspace })),
);
const AdminOrgSSO = lazy(() =>
  import("./pages/admin/AdminOrgSSO").then((m) => ({ default: m.AdminOrgSSO })),
);
const AdminOAuthProviders = lazy(() =>
  import("./pages/admin/AdminOAuthProviders").then((m) => ({ default: m.AdminOAuthProviders })),
);
const AdminPlatform = lazy(() =>
  import("./pages/admin/platform/AdminPlatform").then((m) => ({ default: m.AdminPlatform })),
);
const AdminPlatformDrops = lazy(() =>
  import("./pages/admin/platform/AdminPlatformDrops").then((m) => ({ default: m.AdminPlatformDrops })),
);
const AdminPlatformUsers = lazy(() =>
  import("./pages/admin/platform/AdminPlatformUsers").then((m) => ({ default: m.AdminPlatformUsers })),
);
const AdminPlatformUserDetail = lazy(() =>
  import("./pages/admin/platform/AdminPlatformUserDetail").then((m) => ({ default: m.AdminPlatformUserDetail })),
);
const AdminPlatformOrgs = lazy(() =>
  import("./pages/admin/platform/AdminPlatformOrgs").then((m) => ({ default: m.AdminPlatformOrgs })),
);
const AdminPlatformOrgDetail = lazy(() =>
  import("./pages/admin/platform/AdminPlatformOrgDetail").then((m) => ({ default: m.AdminPlatformOrgDetail })),
);
const AdminPlatformTiers = lazy(() =>
  import("./pages/admin/platform/AdminPlatformTiers").then((m) => ({ default: m.AdminPlatformTiers })),
);
const AdminPlatformSupportAgents = lazy(() =>
  import("./pages/admin/platform/AdminPlatformSupportAgents").then((m) => ({ default: m.AdminPlatformSupportAgents })),
);
const AdminSystemLog = lazy(() =>
  import("./pages/admin/AdminSystemLog").then((m) => ({ default: m.AdminSystemLog })),
);
const AdminGoogle = lazy(() =>
  import("./pages/admin/AdminGoogle").then((m) => ({ default: m.AdminGoogle })),
);
const AdminSecrets = lazy(() =>
  import("./pages/admin/AdminSecrets").then((m) => ({ default: m.AdminSecrets })),
);
const EmailTemplates = lazy(() =>
  import("./pages/admin/EmailTemplates").then((m) => ({ default: m.EmailTemplates })),
);
const AdminGitCredentials = lazy(() =>
  import("./pages/admin/AdminGitCredentials").then((m) => ({ default: m.AdminGitCredentials })),
);
const AdminRunners = lazy(() =>
  import("./pages/admin/AdminRunners").then((m) => ({ default: m.AdminRunners })),
);
const AdminMCPServers = lazy(() =>
  import("./pages/admin/AdminMCPServers").then((m) => ({ default: m.AdminMCPServers })),
);
const AdminWebAPIs = lazy(() =>
  import("./pages/admin/AdminWebAPIs").then((m) => ({ default: m.AdminWebAPIs })),
);
const AdminRunnerDetail = lazy(() =>
  import("./pages/admin/AdminRunnerDetail").then((m) => ({ default: m.AdminRunnerDetail })),
);
const PublicOverview = lazy(() =>
  import("./pages/PublicOverview").then((m) => ({ default: m.PublicOverview })),
);
const PublicCollection = lazy(() =>
  import("./pages/PublicCollection").then((m) => ({ default: m.PublicCollection })),
);


// PageFallback holds the layout still while a route's chunk arrives. It is
// deliberately empty rather than a spinner: on a warm cache the chunk is
// there within a frame, and a spinner that flashes for one frame reads as a
// glitch. A slow network gets the shell (nav, header) and an empty content
// area, which is what a page mid-load should look like.
function PageFallback() {
  return <div className="page-loading" aria-busy="true" />;
}

export function App() {
  const { token } = useAuth();
  const { pathname } = useLocation();
  // Public TV-dashboard share page: cryptic link, no auth, no AppShell.
  // Handled before the signed-in/out split so it renders identically whether
  // or not a session exists (an operator previewing it, or a login-less TV).
  if (pathname.startsWith("/tv/")) {
    return (
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route path="/tv/:token" element={<PublicOverview />} />
        </Routes>
      </Suspense>
    );
  }
  // Public collection table, same deal: the link is the credential, and the
  // person opening it is usually the one who asked for the data rather than
  // anyone with an account here.
  if (pathname.startsWith("/board/")) {
    return (
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route path="/board/:token" element={<PublicCollection />} />
        </Routes>
      </Suspense>
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
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route path="/signup" element={<SignUp />} />
          <Route path="/signin" element={<SignIn />} />
          {/* The verification link can land in a signed-out browser —
              the token is the proof, no session needed. */}
          <Route path="/verify-email" element={<VerifyEmail />} />
          <Route path="/forgot-password" element={<ForgotPassword />} />
          <Route path="/reset-password" element={<ResetPassword />} />
          <Route path="/invite/:token" element={<AcceptInvite />} />
          {/* The bare root is how most people arrive — typing the domain. It has
              no signed-out route of its own, so without this it falls to the
              catch-all below and greets a first-time visitor with a notice about
              the page they asked for. */}
          <Route path="/" element={<SignIn />} />
          {/* Signed-out catch-all, which covers two cases this tree cannot tell
              apart: a mistyped address, and a real app page the visitor is not
              authenticated for (a deep link from an email, a session that
              expired mid-page). So the notice claims only what is true of both —
              that signing in is the next step. Whether the page actually exists
              is decided AFTER sign-in, by the authenticated catch-all, which can
              answer it correctly. */}
          <Route path="*" element={<SignIn signInRequired />} />
        </Routes>
      </Suspense>
    );
  }
  return (
    <UploadsProvider>
    <AppShell>
      <Suspense fallback={<PageFallback />}>
        <Routes>
          <Route path="/" element={<RootRedirect />} />
          {/* /signin and /signup exist only in the signed-OUT tree above, so
              asking for either while signed in used to render the authenticated
              catch-all: a 404 telling the visitor the page does not exist. It
              does; they are simply past it. The sign-up page's own "Sign in"
              link lands here, and so does anyone following a bookmark or a
              stale link from the marketing site.

              A redirect, NOT a sign-out: a successful sign-up sets the token
              while the URL is still /signup, so tearing the session down here
              would log people out the instant they created an account. */}
          <Route path="/signin" element={<Navigate to="/" replace />} />
          <Route path="/signup" element={<Navigate to="/" replace />} />
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
      </Suspense>
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
