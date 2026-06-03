import { createContext, useCallback, useContext, useEffect, useState, ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { api, APIError, setUnauthorizedHandler } from "./api";
import { pickActive } from "./lib/pickActive";
import i18n from "./i18n";
import type { Permission, WhoAmI } from "./types";

const STORAGE_KEY = "hazyflow.token";
const WS_STORAGE_KEY = "hazyflow.activeWorkspace";
const TENANT_STORAGE_KEY = "hazyflow.activeTenant";

type AuthCtx = {
  token: string | null;
  me: WhoAmI | null;
  loading: boolean;
  error: string | null;
  // signInWithPassword resolves to a discriminator: when the account has
  // 2FA enabled the server withholds the session and returns a challenge
  // instead, so the caller must collect a code and finish via verifyTOTP.
  // Errors surface on `error` and are re-thrown.
  signInWithPassword: (
    email: string,
    password: string,
  ) => Promise<{ totpRequired: boolean; challenge?: string }>;
  // verifyTOTP completes leg 2 of sign-in: it exchanges the challenge +
  // a code (or recovery code) for a session, landing the user in the
  // same signed-in state as a code-free sign-in. Pass recoveryCode="" to
  // use a TOTP code and code="" to use a recovery code.
  verifyTOTP: (
    challenge: string,
    code: string,
    recoveryCode: string,
  ) => Promise<void>;
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

  // refreshMe re-fetches the current identity (whoami) and updates `me`,
  // so chrome bound to it — the top bar's org name/logo, the tenant
  // switcher — reflects a just-saved org profile without a full reload.
  refreshMe: () => Promise<void>;
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

  // Register the process-wide 401 handler so a session that expires or is
  // revoked *while the app is open* — not just on the bootstrap whoami —
  // tears down local state, shows the "session expired" message, and
  // bounces to sign-in, instead of leaking the raw backend error into
  // whichever component happened to make the failing request. Registered
  // ahead of the bootstrap effect below so it's live before the first
  // authenticated call resolves. setToken/setMe/setError are stable and
  // navigate is stable across renders, so this runs once.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      localStorage.removeItem(STORAGE_KEY);
      setToken(null);
      setMe(null);
      setError(i18n.t("signIn.sessionExpired"));
      navigate("/signin");
    });
    return () => setUnauthorizedHandler(null);
  }, [navigate]);

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
        // Build the tenant catalogue from three sources in priority
        // order: platform admins see every tenant on the daemon; other
        // users see the orgs they have memberships in (home + invited);
        // a brand-new account with no memberships still sees its home
        // org as the single entry.
        let tenantList: string[] = [];
        if (w.memberships && w.memberships.length > 0) {
          tenantList = w.memberships.map((m) => m.tenant);
        } else if (w.tenant) {
          tenantList = [w.tenant];
        }
        if (isPlatform) {
          try {
            const r = await api.listTenants(token);
            const platformList = r.tenants ?? [];
            // Merge: keep the user's home + invited orgs first
            // (relevant to them), then platform-wide entries after.
            const seen = new Set(tenantList);
            for (const t of platformList) {
              if (!seen.has(t)) {
                tenantList.push(t);
                seen.add(t);
              }
            }
          } catch {
            /* fall back to membership-derived list */
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
      .catch((e: unknown) => {
        if (!cancelled) {
          // A token restored from localStorage that the server rejects
          // (401) means the saved session has expired or been revoked.
          // Show a plain-language message rather than the raw backend
          // "auth: invalid credential" string — most users hitting this
          // are non-technical and just need to know to sign in again.
          // Other failures (network, 5xx) keep their original message.
          const expired = e instanceof APIError && e.status === 401;
          setError(expired ? i18n.t("signIn.sessionExpired") : (e as Error).message);
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
    // For password-auth users, also tell the server to re-issue the
    // session against the new tenant so the next /graphs / /secrets /
    // /admin call lands in the right scope. Platform admins skip this
    // because their session isn't bound to one tenant in the first
    // place; the path here is for invited members switching between
    // their home org and an org they were invited into. Best-effort —
    // a network error is non-fatal, the local state still updates.
    if (token && t && me?.subject?.includes("@")) {
      void api
        .switchOrg(token, t)
        .then(() => api.whoami(token))
        .then((w) => setMe(w))
        .catch(() => {
          /* best-effort; the next whoami refresh will reconcile */
        });
    }
  };

  // refreshMe re-resolves identity from the server and updates `me` so
  // anything bound to it (top bar org name/logo, tenant switcher) reflects
  // a just-saved change. Stable across renders (keyed on token) so callers
  // can safely list it in effect/useCallback deps.
  const refreshMe = useCallback(async () => {
    if (!token) return;
    try {
      setMe(await api.whoami(token));
    } catch {
      /* best-effort; a later whoami reconciles */
    }
  }, [token]);

  // applySession mirrors a freshly-issued session token into localStorage
  // (so the app keeps using its bearer-header path) and resolves identity.
  // Shared by the password, TOTP-second-leg, and signup flows.
  const applySession = async (token: string) => {
    localStorage.setItem(STORAGE_KEY, token);
    setToken(token);
    const who = await api.whoami(token);
    setMe(who);
  };

  const signInWithPassword = async (email: string, password: string) => {
    setLoading(true);
    setError(null);
    try {
      const r = await api.signIn(email, password);
      // 2FA gate: the server returns a challenge instead of a session.
      // Don't apply anything yet — hand the challenge back so the form
      // can switch to the code step. Keep loading off so the second-step
      // inputs are interactive.
      if (r.totp_required && r.challenge) {
        setLoading(false);
        return { totpRequired: true, challenge: r.challenge };
      }
      // The signin endpoint also sets an HttpOnly session cookie, but we
      // mirror the token in localStorage so the rest of the app keeps
      // using its bearer-header code path unchanged.
      await applySession(r.token as string);
      return { totpRequired: false };
    } catch (e) {
      setError((e as Error).message);
      throw e;
    } finally {
      setLoading(false);
    }
  };

  const verifyTOTP = async (
    challenge: string,
    code: string,
    recoveryCode: string,
  ) => {
    setLoading(true);
    setError(null);
    try {
      const r = await api.totpVerify(challenge, code, recoveryCode);
      await applySession(r.token as string);
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
      await applySession(r.token as string);
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
        verifyTOTP,
        signUpWithPassword,
        signOut,
        hasPerm,
        workspaces,
        activeWorkspace,
        setActiveWorkspace,
        tenants,
        activeTenant,
        setActiveTenant,
        refreshMe,
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
