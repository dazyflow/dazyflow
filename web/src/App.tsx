import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "./auth";
import { AppShell } from "./components/AppShell";
import { SignIn } from "./pages/SignIn";
import { PipelineList } from "./pages/PipelineList";
import { PipelineEditor } from "./pages/PipelineEditor";
import { Admin } from "./pages/Admin";

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
        <Route path="/" element={<Navigate to="/pipelines" replace />} />
        <Route path="/pipelines" element={<PipelineList />} />
        <Route path="/pipelines/:id" element={<PipelineEditor />} />
        <Route path="/admin/*" element={<Admin />} />
        <Route path="*" element={<Navigate to="/pipelines" replace />} />
      </Routes>
    </AppShell>
  );
}
