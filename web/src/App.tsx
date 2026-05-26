import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "./auth";
import { AppShell } from "./components/AppShell";
import { SignIn } from "./pages/SignIn";
import { SignUp } from "./pages/SignUp";
import { Welcome } from "./pages/Welcome";
import { Templates } from "./pages/Templates";
import { Integrations, IntegrationDetail } from "./pages/Integrations";
import { FlowList } from "./pages/FlowList";
import { FlowEditor } from "./pages/FlowEditor";
import { RunList } from "./pages/RunList";
import { RunDetail } from "./pages/RunDetail";
import { Approvals } from "./pages/Approvals";
import { Admin } from "./pages/Admin";
import { AdminAPIKeys } from "./pages/AdminAPIKeys";
import { AdminUsers } from "./pages/AdminUsers";

export function App() {
  const { token, loading } = useAuth();
  if (loading && !token) return <div />;
  if (!token) {
    // Unauthenticated: signin/signup are reachable as deep-links;
    // anything else 302s into signin. Two distinct routes so a
    // marketing link pointing at /signup lands on the signup form,
    // not the signin form.
    return (
      <Routes>
        <Route path="/signup" element={<SignUp />} />
        <Route path="/signin" element={<SignIn />} />
        <Route path="*" element={<SignIn />} />
      </Routes>
    );
  }
  return (
    <AppShell>
      <Routes>
        <Route path="/" element={<Navigate to="/flows" replace />} />
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
        <Route path="/integrations" element={<Integrations />} />
        <Route path="/integrations/:slug" element={<IntegrationDetail />} />
        <Route path="/runs" element={<RunList />} />
        <Route path="/runs/:runID" element={<RunDetail />} />
        <Route path="/approvals" element={<Approvals />} />
        <Route path="/admin" element={<Admin />} />
        <Route path="/admin/api-keys" element={<AdminAPIKeys />} />
        <Route path="/admin/users" element={<AdminUsers />} />
        <Route path="*" element={<Navigate to="/flows" replace />} />
      </Routes>
    </AppShell>
  );
}

// LegacyPipelineRedirect picks up the :id from the old path and 301s to
// the canonical /flows/:id, preserving any query string (e.g.
// ?run=<jobID> from a deep-linked run).
import { useLocation, useParams } from "react-router-dom";
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
