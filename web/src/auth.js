import { jsx as _jsx } from "react/jsx-runtime";
import { createContext, useContext, useEffect, useState } from "react";
import { api } from "./api";
import { pickActive } from "./lib/pickActive";
const STORAGE_KEY = "hazyflow.token";
const WS_STORAGE_KEY = "hazyflow.activeWorkspace";
const TENANT_STORAGE_KEY = "hazyflow.activeTenant";
const Ctx = createContext(null);
export function AuthProvider({ children }) {
    const [token, setToken] = useState(() => localStorage.getItem(STORAGE_KEY));
    const [me, setMe] = useState(null);
    const [loading, setLoading] = useState(!!token);
    const [error, setError] = useState(null);
    const [workspaces, setWorkspaces] = useState([]);
    const [activeWorkspace, setActiveWorkspaceState] = useState("");
    const [tenants, setTenants] = useState([]);
    const [activeTenant, setActiveTenantState] = useState("");
    useEffect(() => {
        if (!token) {
            setMe(null);
            setWorkspaces([]);
            setActiveWorkspaceState("");
            setTenants([]);
            setActiveTenantState("");
            return;
        }
        let cancelled = false;
        setLoading(true);
        // Bootstrap: resolve identity + tenant catalog. Workspace loading
        // is a separate effect keyed on activeTenant so a tenant switch
        // triggers a clean refetch (rather than racing the initial pass).
        api
            .whoami(token)
            .then(async (w) => {
            if (cancelled)
                return;
            setMe(w);
            setError(null);
            const isPlatform = w.permissions.includes("platform:admin");
            let tenantList = w.tenant ? [w.tenant] : [];
            if (isPlatform) {
                try {
                    const r = await api.listTenants(token);
                    tenantList = r.tenants ?? [];
                    if (w.tenant && !tenantList.includes(w.tenant)) {
                        tenantList = [w.tenant, ...tenantList];
                    }
                }
                catch {
                    /* fall back to whoami's tenant */
                }
            }
            if (cancelled)
                return;
            setTenants(tenantList);
            const chosenTenant = pickActive(tenantList, localStorage.getItem(TENANT_STORAGE_KEY) ?? "", w.tenant ?? "");
            setActiveTenantState(chosenTenant);
            if (chosenTenant)
                localStorage.setItem(TENANT_STORAGE_KEY, chosenTenant);
        })
            .catch((e) => {
            if (!cancelled) {
                setError(e.message);
                setMe(null);
                localStorage.removeItem(STORAGE_KEY);
                setToken(null);
            }
        })
            .finally(() => {
            if (!cancelled)
                setLoading(false);
        });
        return () => {
            cancelled = true;
        };
    }, [token]);
    // Workspace loader — rerun whenever the active tenant changes so
    // platform admins flipping the tenant switcher see the right list
    // immediately. Picks a sensible activeWorkspace: cached value if
    // it's still in the new tenant's list, else the principal's binding,
    // else the first entry.
    useEffect(() => {
        if (!token || !activeTenant) {
            setWorkspaces([]);
            setActiveWorkspaceState("");
            return;
        }
        let cancelled = false;
        api
            .listWorkspaces(token, activeTenant)
            .then((r) => {
            if (cancelled)
                return;
            const accessible = r.workspaces ?? [];
            setWorkspaces(accessible);
            const chosen = pickActive(accessible, localStorage.getItem(WS_STORAGE_KEY) ?? "", me?.workspace ?? "");
            setActiveWorkspaceState(chosen);
            if (chosen)
                localStorage.setItem(WS_STORAGE_KEY, chosen);
            else
                localStorage.removeItem(WS_STORAGE_KEY);
        })
            .catch(() => {
            /* leave workspaces empty — switcher shows "(pick)" */
        });
        return () => {
            cancelled = true;
        };
        // me is intentionally read inside but excluded from deps —
        // re-running on every me update would double-fetch right after
        // sign-in. The activeTenant dep covers the meaningful transitions.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [token, activeTenant]);
    const setActiveWorkspace = (ws) => {
        setActiveWorkspaceState(ws);
        if (ws)
            localStorage.setItem(WS_STORAGE_KEY, ws);
        else
            localStorage.removeItem(WS_STORAGE_KEY);
    };
    const setActiveTenant = (t) => {
        setActiveTenantState(t);
        if (t)
            localStorage.setItem(TENANT_STORAGE_KEY, t);
        else
            localStorage.removeItem(TENANT_STORAGE_KEY);
        // Switching tenants invalidates the current workspace choice —
        // workspaces are tenant-scoped. Drop it; the post-whoami flow on
        // next reload will pick a sensible default for the new tenant.
        setActiveWorkspaceState("");
        localStorage.removeItem(WS_STORAGE_KEY);
    };
    const signIn = async (newToken) => {
        setLoading(true);
        setError(null);
        try {
            const who = await api.whoami(newToken);
            localStorage.setItem(STORAGE_KEY, newToken);
            setToken(newToken);
            setMe(who);
        }
        catch (e) {
            setError(e.message);
            throw e;
        }
        finally {
            setLoading(false);
        }
    };
    const signOut = () => {
        localStorage.removeItem(STORAGE_KEY);
        localStorage.removeItem(WS_STORAGE_KEY);
        localStorage.removeItem(TENANT_STORAGE_KEY);
        setToken(null);
        setMe(null);
        setWorkspaces([]);
        setActiveWorkspaceState("");
        setTenants([]);
        setActiveTenantState("");
    };
    const hasPerm = (p) => !!me && me.permissions.includes(p);
    return (_jsx(Ctx.Provider, { value: {
            token,
            me,
            loading,
            error,
            signIn,
            signOut,
            hasPerm,
            workspaces,
            activeWorkspace,
            setActiveWorkspace,
            tenants,
            activeTenant,
            setActiveTenant,
        }, children: children }));
}
export function useAuth() {
    const v = useContext(Ctx);
    if (!v)
        throw new Error("useAuth outside AuthProvider");
    return v;
}
