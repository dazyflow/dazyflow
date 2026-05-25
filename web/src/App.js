import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
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
    if (loading && !token)
        return _jsx("div", {});
    if (!token) {
        return (_jsx(Routes, { children: _jsx(Route, { path: "*", element: _jsx(SignIn, {}) }) }));
    }
    return (_jsx(AppShell, { children: _jsxs(Routes, { children: [_jsx(Route, { path: "/", element: _jsx(Navigate, { to: "/flows", replace: true }) }), _jsx(Route, { path: "/flows", element: _jsx(FlowList, {}) }), _jsx(Route, { path: "/flows/:id", element: _jsx(FlowEditor, {}) }), _jsx(Route, { path: "/pipelines", element: _jsx(Navigate, { to: "/flows", replace: true }) }), _jsx(Route, { path: "/pipelines/:id", element: _jsx(LegacyPipelineRedirect, {}) }), _jsx(Route, { path: "/runs", element: _jsx(RunList, {}) }), _jsx(Route, { path: "/approvals", element: _jsx(Approvals, {}) }), _jsx(Route, { path: "/admin", element: _jsx(Admin, {}) }), _jsx(Route, { path: "/admin/api-keys", element: _jsx(AdminAPIKeys, {}) }), _jsx(Route, { path: "/admin/users", element: _jsx(AdminUsers, {}) }), _jsx(Route, { path: "*", element: _jsx(Navigate, { to: "/flows", replace: true }) })] }) }));
}
// LegacyPipelineRedirect picks up the :id from the old path and 301s to
// the canonical /flows/:id, preserving any query string (e.g.
// ?run=<jobID> from a deep-linked run).
import { useLocation, useParams } from "react-router-dom";
function LegacyPipelineRedirect() {
    const { id } = useParams();
    const loc = useLocation();
    return (_jsx(Navigate, { to: { pathname: `/flows/${encodeURIComponent(id ?? "")}`, search: loc.search }, replace: true }));
}
