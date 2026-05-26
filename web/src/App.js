import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
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
    if (loading && !token)
        return _jsx("div", {});
    if (!token) {
        // Unauthenticated: signin/signup are reachable as deep-links;
        // anything else 302s into signin. Two distinct routes so a
        // marketing link pointing at /signup lands on the signup form,
        // not the signin form.
        return (_jsxs(Routes, { children: [_jsx(Route, { path: "/signup", element: _jsx(SignUp, {}) }), _jsx(Route, { path: "/signin", element: _jsx(SignIn, {}) }), _jsx(Route, { path: "*", element: _jsx(SignIn, {}) })] }));
    }
    return (_jsx(AppShell, { children: _jsxs(Routes, { children: [_jsx(Route, { path: "/", element: _jsx(Navigate, { to: "/flows", replace: true }) }), _jsx(Route, { path: "/welcome", element: _jsx(Welcome, {}) }), _jsx(Route, { path: "/flows", element: _jsx(FlowList, {}) }), _jsx(Route, { path: "/flows/:id", element: _jsx(FlowEditor, {}) }), _jsx(Route, { path: "/pipelines", element: _jsx(Navigate, { to: "/flows", replace: true }) }), _jsx(Route, { path: "/pipelines/:id", element: _jsx(LegacyPipelineRedirect, {}) }), _jsx(Route, { path: "/templates", element: _jsx(Templates, {}) }), _jsx(Route, { path: "/integrations", element: _jsx(Integrations, {}) }), _jsx(Route, { path: "/integrations/:slug", element: _jsx(IntegrationDetail, {}) }), _jsx(Route, { path: "/runs", element: _jsx(RunList, {}) }), _jsx(Route, { path: "/runs/:runID", element: _jsx(RunDetail, {}) }), _jsx(Route, { path: "/approvals", element: _jsx(Approvals, {}) }), _jsx(Route, { path: "/admin", element: _jsx(Admin, {}) }), _jsx(Route, { path: "/admin/api-keys", element: _jsx(AdminAPIKeys, {}) }), _jsx(Route, { path: "/admin/users", element: _jsx(AdminUsers, {}) }), _jsx(Route, { path: "*", element: _jsx(Navigate, { to: "/flows", replace: true }) })] }) }));
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
