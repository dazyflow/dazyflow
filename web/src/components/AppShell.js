import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useState } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { Menu, LogOut, Workflow, ShieldCheck, Activity } from "lucide-react";
import { useAuth } from "../auth";
export function AppShell({ children }) {
    const { me, signOut, hasPerm } = useAuth();
    const [navOpen, setNavOpen] = useState(false);
    const location = useLocation();
    // Editor pages need a full-bleed canvas — remove the main padding.
    const inEditor = /^\/pipelines\/[^/]+/.test(location.pathname);
    const showAdmin = hasPerm("tenant:admin") || hasPerm("graph:admin");
    return (_jsxs("div", { className: "app-shell", children: [_jsxs("header", { className: "topbar", children: [_jsx("button", { className: "icon ghost hamburger", onClick: () => setNavOpen((x) => !x), "aria-label": "toggle navigation", children: _jsx(Menu, { size: 20 }) }), _jsxs("div", { className: "brand", children: [_jsx("span", { className: "brand-mark", children: "\u223C" }), _jsx("span", { children: "Hazy Flow" })] }), _jsx("div", { className: "spacer" }), me && (_jsxs("div", { className: "user", children: [_jsx("span", { className: "who", children: me.subject || "(no subject)" }), _jsx("span", { children: "\u00B7" }), _jsxs("span", { children: [me.tenant, "/", me.workspace] }), _jsx("button", { className: "icon ghost", onClick: signOut, "aria-label": "sign out", children: _jsx(LogOut, { size: 18 }) })] }))] }), _jsxs("div", { className: "body", children: [navOpen && (_jsx("div", { className: "sidebar-backdrop", onClick: () => setNavOpen(false) })), _jsxs("aside", { className: "sidebar", "data-open": navOpen ? "true" : "false", children: [_jsx("div", { className: "group-label", children: "Workspace" }), _jsxs(NavLink, { to: "/pipelines", onClick: () => setNavOpen(false), children: [_jsx(Workflow, { size: 18 }), "Pipelines"] }), _jsxs(NavLink, { to: "/runs", onClick: () => setNavOpen(false), children: [_jsx(Activity, { size: 18 }), "Runs"] }), showAdmin && (_jsxs(_Fragment, { children: [_jsx("div", { className: "group-label", children: "Settings" }), _jsxs(NavLink, { to: "/admin", onClick: () => setNavOpen(false), children: [_jsx(ShieldCheck, { size: 18 }), "Admin"] })] }))] }), _jsx("main", { className: "main" + (inEditor ? " no-pad" : ""), children: children })] })] }));
}
