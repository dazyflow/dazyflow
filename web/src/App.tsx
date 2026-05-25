import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "./auth";
import { AppShell } from "./components/AppShell";
import { SignIn } from "./pages/SignIn";
import { FlowList } from "./pages/FlowList";
import { FlowEditor } from "./pages/FlowEditor";
import { RunList } from "./pages/RunList";
import { Approvals } from "./pages/Approvals";
import { Admin } from "./pages/Admin";
import { AdminAPIKeys } from "./pages/AdminAPIKeys";
import { AdminUsers } from "./pages/AdminUsers";

export function App() {
  const { token, loading } = useAuth();
  if (loading && !token) return <div />;
  if (!token) {
    return (
      <Routes>
        <Route path="*" element={<SignIn />} />
      </Routes>
    );
  }
  return (
    <AppShell>
      <Routes>
        <Route path="/" element={<Navigate to="/flows" replace />} />
        <Route path="/flows" element={<FlowList />} />
        <Route path="/flows/:id" element={<FlowEditor />} />
        {/* Legacy /pipelines/* paths — bookmarks from before the rename
            still land in the right place. */}
        <Route path="/pipelines" element={<Navigate to="/flows" replace />} />
        <Route
          path="/pipelines/:id"
          element={<LegacyPipelineRedirect />}
        />
        <Route path="/runs" element={<RunList />} />
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
