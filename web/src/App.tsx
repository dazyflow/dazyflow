import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "./auth";
import { AppShell } from "./components/AppShell";
import { SignIn } from "./pages/SignIn";
import { SignUp } from "./pages/SignUp";
import { Welcome } from "./pages/Welcome";
import { Templates } from "./pages/Templates";
import { Apps, AppDetail } from "./pages/Apps";
import { Secrets } from "./pages/Secrets";
import { FlowList } from "./pages/FlowList";
import { FlowEditor } from "./pages/FlowEditor";
import { RunList } from "./pages/RunList";
import { RunDetail } from "./pages/RunDetail";
import { Approvals } from "./pages/Approvals";
import { Usage } from "./pages/Usage";
import { VerifyEmail } from "./pages/VerifyEmail";
import { Settings } from "./pages/Settings";
import { Admin } from "./pages/Admin";
import { AdminAPIKeys } from "./pages/AdminAPIKeys";
import { AdminUsers } from "./pages/AdminUsers";
import { AdminAudit } from "./pages/AdminAudit";
import { AdminModules } from "./pages/AdminModules";
import { AdminWorkspace } from "./pages/AdminWorkspace";
import { AdminOrgSSO } from "./pages/AdminOrgSSO";
import { AdminOAuthProviders } from "./pages/AdminOAuthProviders";
import { AdminGoogle } from "./pages/AdminGoogle";
import { AdminSecretManager } from "./pages/AdminSecretManager";
import { AcceptInvite } from "./pages/AcceptInvite";

export function App() {
  const { token, loading } = useAuth();
  if (loading && !token) return <div />;
  if (!token) {
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
        <Route path="/invite/:token" element={<AcceptInvite />} />
        <Route path="*" element={<SignIn />} />
      </Routes>
    );
  }
  return (
    <AppShell>
      <Routes>
        <Route path="/" element={<RootRedirect />} />
        <Route path="/welcome" element={<Welcome />} />
        <Route path="/flows" element={<FlowList />} />
        <Route path="/flows/:id" element={<FlowEditor />} />
        {/* Legacy /pipelines/* paths — bookmarks from before the rename
            still land in the right place. */}
        <Route path="/pipelines" element={<Navigate to="/flows" replace />} />
        <Route
          path="/pipelines/:id"
          element={<LegacyPipelineRedirect />}
        />
        <Route path="/templates" element={<Templates />} />
        <Route path="/secrets" element={<Secrets />} />
        <Route path="/apps" element={<Apps />} />
        <Route path="/apps/:slug" element={<AppDetail />} />
        <Route path="/runs" element={<RunList />} />
        <Route path="/runs/:runID" element={<RunDetail />} />
        <Route path="/approvals" element={<Approvals />} />
        <Route path="/usage" element={<Usage />} />
        <Route path="/verify-email" element={<VerifyEmail />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/admin" element={<Admin />} />
        <Route path="/admin/api-keys" element={<AdminAPIKeys />} />
        <Route path="/admin/users" element={<AdminUsers />} />
        <Route path="/admin/audit" element={<AdminAudit />} />
        <Route path="/admin/modules" element={<AdminModules />} />
        <Route path="/admin/workspace" element={<AdminWorkspace />} />
        <Route path="/admin/sso" element={<AdminOrgSSO />} />
        <Route path="/admin/oauth" element={<AdminOAuthProviders />} />
        <Route path="/admin/google" element={<AdminGoogle />} />
        <Route path="/admin/secret-manager" element={<AdminSecretManager />} />
        <Route path="/invite/:token" element={<AcceptInvite />} />
        <Route path="*" element={<Navigate to="/flows" replace />} />
      </Routes>
    </AppShell>
  );
}

// LegacyPipelineRedirect picks up the :id from the old path and 301s to
// the canonical /flows/:id, preserving any query string (e.g.
// ?run=<jobID> from a deep-linked run).
import { useLocation, useParams } from "react-router-dom";

// HAS_FLOWS_KEY is FlowList's sticky "this user has built at least
// one flow" hint. RootRedirect reads it (and only it — no API call)
// to decide whether the bare-root visit should land on /welcome
// (first-time) or /flows (returning). Written by FlowList itself
// when the graph list resolves; cleared when it resolves empty.
const HAS_FLOWS_KEY = "hazyflow.hasFlows";

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
  if (loc.search) {
    return <Navigate to={{ pathname: "/flows", search: loc.search }} replace />;
  }
  let hasFlows = false;
  try {
    hasFlows = localStorage.getItem(HAS_FLOWS_KEY) === "1";
  } catch {
    /* private mode / strict iframe — treat as first-time */
  }
  return <Navigate to={hasFlows ? "/flows" : "/welcome"} replace />;
}

function LegacyPipelineRedirect() {
  const { id } = useParams();
  const loc = useLocation();
  return (
    <Navigate
      to={{ pathname: `/flows/${encodeURIComponent(id ?? "")}`, search: loc.search }}
      replace
    />
  );
}
