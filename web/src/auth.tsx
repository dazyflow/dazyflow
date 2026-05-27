import { createContext, useContext, useEffect, useState, ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "./api";
import { pickActive } from "./lib/pickActive";
import type { Permission, WhoAmI } from "./types";

const STORAGE_KEY = "hazyflow.token";
const WS_STORAGE_KEY = "hazyflow.activeWorkspace";
const TENANT_STORAGE_KEY = "hazyflow.activeTenant";

type AuthCtx = {
  token: string | null;
  me: WhoAmI | null;
  loading: boolean;
  error: string | null;
  signInWithPassword: (email: string, password: string) => Promise<void>;
  // signUpWithPassword creates a new account, auto-signs the user in,
  // and lands them in the same authenticated state as a sign-in call.
  // Errors surface on the context (`error`) and are re-thrown so the
  // caller can branch on success.
  signUpWithPassword: (email: string, password: string) => Promise<void>;
  signOut: () => Promise<void>;
  hasPerm: (p: Permission) => boolean;
  // Workspace state. `workspaces` is the list the principal can access
  // (single entry for scoped keys; many for tenant admins).
  // `activeWorkspace` is what the UI's pages should query against — it
  // defaults to me.workspace, or the first listed entry for admins
  // whose key isn't bound to one workspace.
  workspaces: string[];
  activeWorkspace: string;
  setActiveWorkspace: (ws: string) => void;

  // Tenant state. For platform admins (no tenant binding), `tenants`
  // lists every tenant on the hzd instance and `activeTenant` is
  // their current selection. For everyone else, `tenants` is the
  // singleton of their own tenant and the switcher hides.
  tenants: string[];
  activeTenant: string;
  setActiveTenant: (t: string) => void;
};

const Ctx = createContext<AuthCtx | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(() =>
    localStorage.getItem(STORAGE_KEY),
  );
  const [me, setMe] = useState<WhoAmI | null>(null);
  const [loading, setLoading] = useState<boolean>(!!token);
  const [error, setError] = useState<string | null>(null);
  const [workspaces, setWorkspaces] = useState<string[]>([]);
  const [activeWorkspace, setActiveWorkspaceState] = useState<string>("");
  const [tenants, setTenants] = useState<string[]>([]);
  const [activeTenant, setActiveTenantState] = useState<string>("");
  const navigate = useNavigate();

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
        if (cancelled) return;
        setMe(w);
        setError(null);
        const isPlatform = w.permissions.includes("platform:admin");
        let tenantList: string[] = w.tenant ? [w.tenant] : [];
        if (isPlatform) {
          try {
            const r = await api.listTenants(token);
            tenantList = r.tenants ?? [];
            if (w.tenant && !tenantList.includes(w.tenant)) {
              tenantList = [w.tenant, ...tenantList];
            }
          } catch {
            /* fall back to whoami's tenant */
          }
        }
        if (cancelled) return;
        setTenants(tenantList);
        const chosenTenant = pickActive(
          tenantList,
          localStorage.getItem(TENANT_STORAGE_KEY) ?? "",
          w.tenant ?? "",
        );
        setActiveTenantState(chosenTenant);
        if (chosenTenant) localStorage.setItem(TENANT_STORAGE_KEY, chosenTenant);
      })
      .catch((e: Error) => {
        if (!cancelled) {
          setError(e.message);
          setMe(null);
          localStorage.removeItem(STORAGE_KEY);
          setToken(null);
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
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
        if (cancelled) return;
        const accessible = r.workspaces ?? [];
        setWorkspaces(accessible);
        const chosen = pickActive(
          accessible,
          localStorage.getItem(WS_STORAGE_KEY) ?? "",
          me?.workspace ?? "",
        );
        setActiveWorkspaceState(chosen);
        if (chosen) localStorage.setItem(WS_STORAGE_KEY, chosen);
        else localStorage.removeItem(WS_STORAGE_KEY);
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

  const setActiveWorkspace = (ws: string) => {
    setActiveWorkspaceState(ws);
    if (ws) localStorage.setItem(WS_STORAGE_KEY, ws);
    else localStorage.removeItem(WS_STORAGE_KEY);
  };

  const setActiveTenant = (t: string) => {
    setActiveTenantState(t);
    if (t) localStorage.setItem(TENANT_STORAGE_KEY, t);
    else localStorage.removeItem(TENANT_STORAGE_KEY);
    // Switching tenants invalidates the current workspace choice —
    // workspaces are tenant-scoped. Drop it; the post-whoami flow on
    // next reload will pick a sensible default for the new tenant.
    setActiveWorkspaceState("");
    localStorage.removeItem(WS_STORAGE_KEY);
  };

  const signInWithPassword = async (email: string, password: string) => {
    setLoading(true);
    setError(null);
    try {
      const r = await api.signIn(email, password);
      // The signin endpoint also sets an HttpOnly session cookie, but
      // we mirror the token in localStorage so the rest of the app
      // keeps using its bearer-header code path unchanged.
      localStorage.setItem(STORAGE_KEY, r.token);
      setToken(r.token);
      const who = await api.whoami(r.token);
      setMe(who);
    } catch (e) {
      setError((e as Error).message);
      throw e;
    } finally {
      setLoading(false);
    }
  };

  // signUpWithPassword: same wire shape as signInWithPassword (the
  // backend issues a session immediately on signup), so we can
  // collapse the two code paths after the initial API call.
  const signUpWithPassword = async (email: string, password: string) => {
    setLoading(true);
    setError(null);
    try {
      const r = await api.signUp(email, password);
      localStorage.setItem(STORAGE_KEY, r.token);
      setToken(r.token);
      const who = await api.whoami(r.token);
      setMe(who);
    } catch (e) {
      setError((e as Error).message);
      throw e;
    } finally {
      setLoading(false);
    }
  };

  const signOut = async () => {
    // Await the server-side session delete before clearing local state, so
    // the session cookie is actually expired by the time we navigate. The
    // landing gate at / is cookie-based (hasValidSession), so a still-live
    // cookie would otherwise serve the app shell to a just-logged-out user.
    // Failure is non-fatal — we clear local state regardless.
    const t = token;
    if (t) {
      try {
        await api.signOut(t);
      } catch {
        /* ignored — local state still gets cleared below */
      }
    }
    localStorage.removeItem(STORAGE_KEY);
    localStorage.removeItem(WS_STORAGE_KEY);
    localStorage.removeItem(TENANT_STORAGE_KEY);
    setToken(null);
    setMe(null);
    setWorkspaces([]);
    setActiveWorkspaceState("");
    setTenants([]);
    setActiveTenantState("");
    // Leave the protected path we were on (e.g. /admin) — otherwise the
    // URL stays put and just re-renders as the sign-in form under a stale
    // path. Root renders SignIn when logged out, so this is a clean reset.
    navigate("/", { replace: true });
  };

  const hasPerm = (p: Permission) =>
    !!me && me.permissions.includes(p);

  return (
    <Ctx.Provider
      value={{
        token,
        me,
        loading,
        error,
        signInWithPassword,
        signUpWithPassword,
        signOut,
        hasPerm,
        workspaces,
        activeWorkspace,
        setActiveWorkspace,
        tenants,
        activeTenant,
        setActiveTenant,
      }}
    >
      {children}
    </Ctx.Provider>
  );
}

export function useAuth(): AuthCtx {
  const v = useContext(Ctx);
  if (!v) throw new Error("useAuth outside AuthProvider");
  return v;
}
