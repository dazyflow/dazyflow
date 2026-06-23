import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "./auth";
import { userScope } from "./recentFlow";
import { AppShell } from "./components/AppShell";
import { SignIn } from "./pages/SignIn";
import { SignUp } from "./pages/SignUp";
import { Welcome } from "./pages/Welcome";
import { Dashboard } from "./pages/Dashboard";
import { CreateFlow } from "./pages/CreateFlow";
import { Apps, AppDetail } from "./pages/Apps";
import { Files } from "./pages/Files";
import { FlowList } from "./pages/FlowList";
import { FlowEditor } from "./pages/FlowEditor";
import { RunList } from "./pages/RunList";
import { RunDetail } from "./pages/RunDetail";
import { Results } from "./pages/Results";
import { Approvals } from "./pages/Approvals";
import { Usage } from "./pages/Usage";
import { VerifyEmail } from "./pages/VerifyEmail";
import { ForgotPassword } from "./pages/ForgotPassword";
import { ResetPassword } from "./pages/ResetPassword";
import { Settings } from "./pages/Settings";
import { Admin } from "./pages/Admin";
import { AdminAPIKeys } from "./pages/AdminAPIKeys";
import { AdminUsers } from "./pages/AdminUsers";
import { AdminAudit } from "./pages/AdminAudit";
import { AdminWorkspace } from "./pages/AdminWorkspace";
import { AdminOrgSSO } from "./pages/AdminOrgSSO";
import { AdminOAuthProviders } from "./pages/AdminOAuthProviders";
import { AdminPlatform } from "./pages/AdminPlatform";
import { AdminGoogle } from "./pages/AdminGoogle";
import { AdminSecrets } from "./pages/AdminSecrets";
import { AdminGitCredentials } from "./pages/AdminGitCredentials";
import { AcceptInvite } from "./pages/AcceptInvite";
import { UploadsProvider } from "./uploads";

export function App() {
  const { token } = useAuth();
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
        <Route path="*" element={<SignIn />} />
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
        <Route path="/results" element={<Results />} />
        <Route path="/runs/:runID" element={<RunDetail />} />
        <Route path="/approvals" element={<Approvals />} />
        <Route path="/usage" element={<Usage />} />
        <Route path="/verify-email" element={<VerifyEmail />} />
        <Route path="/reset-password" element={<ResetPassword />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/admin" element={<Admin />} />
        <Route path="/admin/api-keys" element={<AdminAPIKeys />} />
        <Route path="/admin/users" element={<AdminUsers />} />
        <Route path="/admin/audit" element={<AdminAudit />} />
        <Route path="/admin/workspace" element={<AdminWorkspace />} />
        <Route path="/admin/sso" element={<AdminOrgSSO />} />
        <Route path="/admin/oauth" element={<AdminOAuthProviders />} />
        <Route path="/admin/platform" element={<AdminPlatform />} />
        <Route path="/admin/google" element={<AdminGoogle />} />
        <Route path="/admin/secrets" element={<AdminSecrets />} />
        <Route path="/admin/git-credentials" element={<AdminGitCredentials />} />
        <Route path="/invite/:token" element={<AcceptInvite />} />
        <Route path="*" element={<Navigate to="/flows" replace />} />
      </Routes>
    </AppShell>
    </UploadsProvider>
  );
}

import { useLocation, useParams } from "react-router-dom";

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
function RootRedirect() {
  const loc = useLocation();
  const { me } = useAuth();
  if (loc.search) {
    return <Navigate to={{ pathname: "/flows", search: loc.search }} replace />;
  }
  // The flag is per-account (see userScope), so we need `me` before we
  // can read it. whoami resolves moments after the token gate above —
  // render nothing for that beat instead of misrouting.
  if (!me) return <div />;
  let hasFlows = false;
  try {
    hasFlows =
      localStorage.getItem(`${HAS_FLOWS_KEY}.${userScope(me)}`) === "1";
  } catch {
    /* private mode / strict iframe — treat as first-time */
  }
  // Returning users (already have flows) land on the workspace overview —
  // the "is everything healthy?" dashboard. First-timers still get the
  // onboarding wizard.
  return <Navigate to={hasFlows ? "/overview" : "/welcome"} replace />;
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
