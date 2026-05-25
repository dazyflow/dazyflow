import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "./auth";
import { AppShell } from "./components/AppShell";
import { SignIn } from "./pages/SignIn";
import { PipelineList } from "./pages/PipelineList";
import { PipelineEditor } from "./pages/PipelineEditor";
import { RunList } from "./pages/RunList";
import { Admin } from "./pages/Admin";
export function App() {
    const { token, loading } = useAuth();
    if (loading && !token)
        return _jsx("div", {});
    if (!token) {
        return (_jsx(Routes, { children: _jsx(Route, { path: "*", element: _jsx(SignIn, {}) }) }));
    }
    return (_jsx(AppShell, { children: _jsxs(Routes, { children: [_jsx(Route, { path: "/", element: _jsx(Navigate, { to: "/pipelines", replace: true }) }), _jsx(Route, { path: "/pipelines", element: _jsx(PipelineList, {}) }), _jsx(Route, { path: "/pipelines/:id", element: _jsx(PipelineEditor, {}) }), _jsx(Route, { path: "/runs", element: _jsx(RunList, {}) }), _jsx(Route, { path: "/admin/*", element: _jsx(Admin, {}) }), _jsx(Route, { path: "*", element: _jsx(Navigate, { to: "/pipelines", replace: true }) })] }) }));
}
